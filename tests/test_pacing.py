"""Pacing: the Administration paces its rooms to the machine. One contract
with three users once set up every room at once, two apps each, and the
machine stopped answering for twenty minutes while the seats' coins ran
out waiting; then one setup at a time left a machine with memory to spare
setting up rooms in single file. Run through tests/isolated.sh.
"""
import json
import os
import subprocess
import time
import unittest

from test_administration import Base, SETUP, professional, running, write, setUpModule  # noqa: F401

GB = 1 << 20  # in kB, as /proc/meminfo counts


class Pacing(Base):
    def stamp_setup(self, log, sleep, extra=""):
        """The house's setup, writing when each room's setup starts and ends."""
        stamp = "python3 -c 'import time; print(time.time())'"
        write(os.path.join(self.project, ".administration", "setup"),
              f'#!/bin/sh\necho "start $ADMINISTRATION_PROFESSIONAL $({stamp})" >> "{log}"\n{extra}sleep {sleep}\n'
              f'echo "end $ADMINISTRATION_PROFESSIONAL $({stamp})" >> "{log}"\n' + SETUP.split("\n", 1)[1], 0o755)

    def test_setups_take_turns_and_the_clock_starts_when_the_room_is_ready(self):
        self.house()
        log = os.path.join(self.tmp, "setups")
        self.stamp_setup(log, 1.5)
        for n in ("a", "b", "c"):
            self.hire_file(n, fake="slow")
        # Each setup takes 1.5s and the limit is 2.5s. The house was never
        # measured, so the first goes alone; the other two wait about 1.5s
        # for it, which mustn't count against their limit, then go together.
        env = {"ADMINISTRATION_SETUP_TIMEOUT": "2500ms", "ADMINISTRATION_MAX_SETUPS": "2"}
        cid = self.place("x", "--cast", "a,b,c", env=env)
        said = set()
        deadline = time.time() + 30
        while time.time() < deadline:
            st = self.status(cid)["status"]
            said |= {s.get("reason", "") for s in st.values()}
            if all(s["state"] in ("working", "out", "back") for s in st.values()):
                break
            time.sleep(0.1)
        self.assertTrue([w for w in said if w.startswith("waiting for setup (")], said)
        self.assertIn("setting up", said)
        self.cli("wait", cid)
        spans = {}
        for line in open(log).read().split("\n"):
            if line:
                what, who, t = line.split()
                spans.setdefault(who, {})[what] = float(t)
        self.assertEqual(sorted(spans), ["a", "b", "c"])
        first, two, three = sorted(spans.values(), key=lambda s: s["start"])
        # The first alone, measured; then the other two side by side.
        self.assertLessEqual(first["end"], two["start"], spans)
        self.assertLess(three["start"], two["end"], spans)
        st = self.status(cid)["status"]
        last = max(st.values(), key=lambda s: s.get("waited_secs", 0))
        self.assertGreaterEqual(last["waited_secs"], 1, last)
        for name, s in st.items():
            # Out of coins, every one: a full purse (1 coin is 2s here) from
            # when its room was ready, however long it waited for its turn.
            self.assertEqual(s["state"], "out", (name, s))
            ready = time.mktime(time.strptime(s["ready_at"], "%Y-%m-%dT%H:%M:%SZ"))
            done = time.mktime(time.strptime(s["finished_at"], "%Y-%m-%dT%H:%M:%SZ"))
            self.assertGreaterEqual(done - ready, 1, s)
            self.assertLessEqual(done - ready, 5, s)
        self.cli("close", cid)
        report = self.cli("report", cid).stdout
        self.assertIn("| waited |", report)
        self.assertRegex(report, r"\| [abc] \| user \| out[^\n]*\| \d+s \|")

    def test_one_setup_at_a_time_when_the_machine_says_so(self):
        self.house()
        log = os.path.join(self.tmp, "setups")
        self.stamp_setup(log, 0.5)
        for n in ("a", "b", "c"):
            self.hire_file(n)
        cid = self.place("x", "--cast", "a,b,c", env={"ADMINISTRATION_MAX_SETUPS": "1"})
        self.cli("wait", cid)
        spans = {}
        for line in open(log).read().split("\n"):
            if line:
                what, who, t = line.split()
                spans.setdefault(who, {})[what] = float(t)
        order = sorted(spans.values(), key=lambda s: s["start"])
        for one, two in zip(order, order[1:]):
            self.assertLessEqual(one["end"], two["start"], spans)

    def test_the_before_build_is_made_once_for_every_room_and_reused(self):
        g = lambda *a: subprocess.run(["git", *a], cwd=self.project, check=True, env=self.env, capture_output=True, text=True).stdout.strip()
        self.house()
        count = os.path.join(self.tmp, "built")
        self.env["BUILD_COUNT"] = count
        adm = os.path.join(self.project, ".administration")
        write(os.path.join(adm, "roster.toml"), '[setup]\npass_env = ["ROOM_DATA", "ROOM_ORIGIN", "BUILD_COUNT"]\n')
        write(os.path.join(adm, "build"), '#!/bin/sh\nset -e\necho "$1" >> "$BUILD_COUNT"\ncat app.txt > "$2/app"\necho "$2/app"\n', 0o755)
        write(os.path.join(self.project, "app.txt"), "before\n")
        g("add", "app.txt"), g("commit", "-qm", "base")
        base = g("rev-parse", "HEAD")
        g("checkout", "-qb", "change")
        write(os.path.join(self.project, "app.txt"), "after\n")
        g("commit", "-qam", "the change")
        head = g("rev-parse", "HEAD")
        self.hire_file("one")
        self.hire_file("two")
        cid = self.place("these changes", "--cast", "one,two", "--changes", "--base", "main")
        self.cli("wait", cid)
        for name in ("one", "two"):
            s = self.status(cid)["status"][name]
            self.assertEqual(s["state"], "back", s)
            self.assertEqual(s["before_build_commit"], base, s)
            env = open(os.path.join(s["room"], "before", "setup-env")).read()
            self.assertIn("ADMINISTRATION_SIDE=before", env)
            self.assertIn("ADMINISTRATION_BUILD_COMMIT=" + base, env)
            built = [l.split("=", 1)[1] for l in env.split("\n") if l.startswith("ADMINISTRATION_BUILD=")]
            self.assertEqual(open(built[0]).read(), "before\n")  # the merge base's own
            after = open(os.path.join(s["room"], "app", "setup-env")).read()
            self.assertIn("ADMINISTRATION_BUILD_COMMIT=" + head, after)
        # Each commit built once, for both rooms.
        self.assertEqual(sorted(open(count).read().split()), sorted([base, head]))
        self.cli("close", cid)
        # The next contract on the same base takes the same builds.
        cid = self.place("these changes", "--cast", "one", "--changes", "--base", "main")
        self.cli("wait", cid)
        self.assertEqual(self.status(cid)["status"]["one"]["state"], "back")
        self.assertEqual(sorted(open(count).read().split()), sorted([base, head]))
        # An open contract's before build is kept by prune, as its own is.
        self.cli("prune", "--older", "0s")
        import glob
        kept = {os.path.basename(d) for d in glob.glob(os.path.join(self.books, "builds", "*", "*"))}
        self.assertEqual(kept, {base, head})

    def test_without_a_build_hook_a_heavy_setup_is_told_to_get_one(self):
        self.house()
        self.set_memory(12 * GB, 16 * GB)
        write(os.path.join(self.project, ".administration", "setup"),
              self.drop_memory(3) + SETUP.split("\n", 1)[1], 0o755)
        self.hire_file("slow", fake="slow", coins="30m")
        cid = self.place("x", "--cast", "slow")
        self.wait_state(cid, "slow", {"working"})
        out = self.cli("status", cid).stdout
        self.assertIn("Worth a build hook: this house's room setup is heavy", out)
        self.assertIn("3.0 GB of memory", out)

    def drop_memory(self, gb):
        """A setup's first lines: it takes gb of the machine's memory, as
        the fake meminfo tells it."""
        return ("#!/bin/sh\npython3 - <<'PY'\n"
                f"p = {self.meminfo!r}\n"
                "lines = open(p).read().split('\\n')\n"
                f"out = [('MemAvailable: %d kB' % (int(l.split()[1]) - {gb * GB})) if l.startswith('MemAvailable:') else l for l in lines]\n"
                "open(p + '.s', 'w').write('\\n'.join(out))\n"
                "import os; os.replace(p + '.s', p)\n"
                "PY\nsleep 1\n")

    def test_a_room_waits_for_memory_and_starts_when_it_frees(self):
        self.house()
        # 16 GB: 2.4 GB of it kept for the machine.
        self.set_memory(12 * GB, 16 * GB)
        write(os.path.join(self.project, ".administration", "setup"),
              self.drop_memory(4) + SETUP.split("\n", 1)[1], 0o755)
        self.hire_file("slow", fake="slow", coins="30m")
        self.hire_file("member")
        first = self.place("x", "--cast", "slow")
        self.wait_state(first, "slow", {"working"})  # measured: its setup took 4 GB
        # 5 GB free is 2.6 over the reserve: not enough for another.
        self.set_memory(5 * GB, 16 * GB)
        second = self.place("x", "--cast", "member")
        deadline = time.time() + 20
        while "waiting for memory" not in self.status(second)["status"]["member"].get("reason", ""):
            self.assertLess(time.time(), deadline, self.status(second))
            time.sleep(0.1)
        self.assertIn("this house's rooms take about 4.0 GB to set up", self.cli("status", second).stdout)
        time.sleep(1)
        self.assertEqual(self.status(second)["status"]["member"]["state"], "waiting")
        self.set_memory(12 * GB, 16 * GB)
        self.cli("wait", second)
        s = self.status(second)["status"]["member"]
        self.assertEqual(s["state"], "back", s)
        self.assertGreaterEqual(s["waited_secs"], 1)
        self.cli("close", first)

    def test_a_contract_over_the_free_rooms_queues_and_starts_itself(self):
        self.house()
        env = {"ADMINISTRATION_MAX_ROOMS": "1"}
        self.env.update(env)
        self.hire_file("slow", fake="slow", coins="30m")
        self.hire_file("member")
        self.hire_file("other")
        first = self.place("x", "--cast", "slow", env={"GIT_AUTHOR_NAME": "Sam"})
        self.wait_state(first, "slow", {"working"})
        p = self.cli("place", "y", "--cast", "member")
        second = p.stdout.split()[1]
        self.assertIn(f"Queued, 1 ahead: 1 of 1 rooms are in use (contract {first}, placed by Sam).", p.stdout)
        self.assertIn(f"administration wait {second}", p.stdout)
        p = self.cli("place", "z", "--cast", "other")
        third = p.stdout.split()[1]
        self.assertIn("Queued, 2 ahead", p.stdout)
        self.assertEqual(self.wait_state(second, "member", {"waiting"})["reason"], "waiting for a room (all 1 in use)")
        self.assertEqual(self.wait_state(third, "other", {"waiting"})["reason"], "waiting for a room (1 ahead)")
        self.assertIn("Rooms: 1 of 1 in use, 0 free. 2 users are waiting for one.", self.cli("status").stdout)
        # The old refusal, for a script that would rather try again.
        p = self.cli("place", "w", "--cast", "member", "--no-queue", check=4)
        self.assertIn("1 of 1 rooms are in use and 2 users are waiting for one", p.stderr)
        # More users than rooms never fits, queue or not.
        self.assertIn("needs 2 rooms and there are 1", self.cli("place", "v", "--cast", "member,other", check=1).stderr)
        time.sleep(1)
        self.assertEqual(self.status(second)["status"]["member"]["state"], "waiting")
        # Once the room frees, they start by themselves, in the order placed.
        self.cli("close", first)
        self.cli("wait", second)
        self.cli("wait", third)
        a, b = self.status(second)["status"]["member"], self.status(third)["status"]["other"]
        self.assertEqual((a["state"], b["state"]), ("back", "back"))
        self.assertLessEqual(a["ready_at"], b["ready_at"])
        # One room: the third's room is ready only once the second's is done.
        # (Their waits are close: the third was placed later.)
        self.assertGreaterEqual(b["ready_at"], a["finished_at"])
        self.assertGreater(b["waited_secs"], 0)

    def test_a_setup_that_times_out_leaves_no_container(self):
        self.house()
        pidfile = os.path.join(self.tmp, "container.pid")
        # A "container": started for the setup by something outside its
        # process group, with nothing of the setup's environment, so only
        # the cleanup the setup declared can reach it.
        write(os.path.join(self.project, ".administration", "setup"), f'''
            #!/bin/sh
            python3 - <<'PY'
            import os
            pid = os.fork()
            if pid == 0:
                os.setsid()
                child = os.fork()
                if child == 0:
                    os.execve("/bin/sleep", ["sleep", "300"], {{}})
                open({pidfile!r}, "w").write(str(child))
                os._exit(0)
            os.waitpid(pid, 0)
            PY
            echo "kill $(cat {pidfile})" >> "$ADMINISTRATION_CLEANUP"
            sleep 60
            ''', 0o755)
        self.hire_file("member")
        # Place may hear of it at once, or wait may.
        cid = self.cli("place", "x", "--cast", "member", env={"ADMINISTRATION_SETUP_TIMEOUT": "2s"}, check=None).stdout.split()[1]
        self.cli("wait", cid, check=2)
        s = self.status(cid)["status"]["member"]
        self.assertIn("setup took longer than 2s", s["reason"])
        pid = int(open(pidfile).read())
        deadline = time.time() + 5
        while running(pid) and time.time() < deadline:
            time.sleep(0.1)
        self.assertFalse(running(pid), "the setup's container outlived its room")
        self.assertIn("cleanup: kill", open(os.path.join(self.pdir(cid, "member"), "teardown.log")).read())


class PersonsHooks(Base):
    def test_the_persons_hook_runs_in_its_own_folder_and_a_failure_is_said(self):
        cfg = os.path.join(self.tmp, "config")
        self.env["ADMINISTRATION_CONFIG"] = cfg
        got = os.path.join(self.tmp, "where")
        # The project's folder can configure tools (a version manager's
        # config the person never trusted) that the person's own hook would
        # then run under; it runs in its own folder instead.
        write(os.path.join(cfg, "hooks", "closed"), f'#!/bin/sh\npwd > "{got}.machine"\nexit 4\n', 0o755)
        write(os.path.join(self.project, ".administration", "hooks", "closed"), f'#!/bin/sh\npwd > "{got}.project"\n', 0o755)
        self.hire_file("r", kind="critic")
        cid = self.place("x", "--cast", "r")
        self.cli("wait", cid)
        p = self.cli("close", cid)
        self.assertEqual(open(got + ".machine").read().strip(), os.path.realpath(os.path.join(cfg, "hooks")))
        self.assertEqual(os.path.realpath(open(got + ".project").read().strip()), os.path.realpath(self.project))
        self.assertIn("the closed hook", p.stderr)
        self.assertIn("failed (exit status 4)", p.stderr)
        self.assertIn("exited 4", open(os.path.join(self.books, "hooks.log")).read())



class HandingOn(Base):
    """A room takes minutes to set up and seconds to put back: a user who's
    done hands its room on to the next user waiting for one."""

    def house_with_reset(self, reset_body="exit 0"):
        self.house()
        log = os.path.join(self.tmp, "setups")
        adm = os.path.join(self.project, ".administration")
        setup = open(os.path.join(adm, "setup")).read()
        write(os.path.join(adm, "setup"), setup.replace("set -e\n", f'set -e\necho "setup $ADMINISTRATION_PROFESSIONAL" >> "{log}"\n', 1), 0o755)
        write(os.path.join(adm, "reset"),
              f'#!/bin/sh\necho "reset $ADMINISTRATION_PROFESSIONAL $2" >> "{log}"\n'
              'echo left > "$ADMINISTRATION_ROOM/desk/left-behind"\n' + reset_body + "\n", 0o755)
        return log

    def test_a_finished_room_is_put_back_and_handed_on(self):
        log = self.house_with_reset()
        self.hire_file("a")
        self.hire_file("b")
        cid = self.place("x", "--cast", "a,b", "--rooms", "a>b", env={"ADMINISTRATION_MAX_ROOMS": "1"})
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        a, b = st["a"], st["b"]
        self.assertEqual((a["state"], b["state"]), ("back", "back"), st)
        lines = open(log).read().split("\n")
        # One setup, a's; one reset, by a, of the room's app; b took it.
        self.assertEqual([l for l in lines if l.startswith("setup")], ["setup a"], lines)
        self.assertEqual([l.split()[:2] for l in lines if l.startswith("reset")], [["reset", "a"]], lines)
        self.assertEqual(a.get("handed_to"), "b", a)
        self.assertEqual(b.get("handed_from"), "a", b)
        self.assertEqual(b.get("resets"), 1, b)
        self.assertEqual(b["room"], a["room"])
        self.assertTrue(a["torn_down"] and b["torn_down"], st)
        # The room was torn down once, by its last user, and what a left
        # on the desk was cleared before b came in.
        rooms = os.path.dirname(a["room"])
        self.assertFalse(os.path.exists(os.path.join(a["room"], "a.torn")) or os.path.exists(os.path.join(rooms, "a.torn")))
        self.assertTrue(os.path.exists(os.path.join(a["room"], "b.torn")), os.listdir(a["room"]))
        self.assertFalse(os.path.exists(os.path.join(a["room"], "desk", "left-behind")))
        # b's coins started when it took the room, and it waited for it.
        self.assertIn("ready_at", b)
        out = self.cli("status", cid).stdout
        self.assertIn("in a's room, put back for it", out)
        self.assertIn("handed its room on to b", out)
        self.cli("close", cid)
        self.assertIn("| a's, put back |", self.cli("report", cid).stdout)

    def test_a_room_whose_reset_fails_is_torn_down_and_the_next_sets_up_its_own(self):
        log = self.house_with_reset("exit 3")
        self.hire_file("a")
        self.hire_file("b")
        cid = self.place("x", "--cast", "a,b", "--rooms", "a>b", env={"ADMINISTRATION_MAX_ROOMS": "1"})
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertEqual((st["a"]["state"], st["b"]["state"]), ("back", "back"), st)
        lines = open(log).read().split("\n")
        self.assertEqual([l for l in lines if l.startswith("setup")], ["setup a", "setup b"], lines)
        self.assertNotIn("handed_to", st["a"])
        self.assertNotIn("handed_from", st["b"])
        self.assertIn("the room isn't handed on", open(os.path.join(self.pdir(cid, "a"), "reset.log")).read())
        self.cli("close", cid)

    def test_users_the_plan_doesnt_put_in_turns_keep_their_own_rooms(self):
        log = self.house_with_reset()
        self.hire_file("a")
        self.hire_file("b")
        # A reset hook, but no plan: each works apart, as ever.
        p = self.cli("place", "x", "--cast", "a,b", check=1, env={"ADMINISTRATION_MAX_ROOMS": "1"})
        self.assertIn("needs 2 rooms and there are 1", p.stderr)
        self.assertIn("--rooms", p.stderr)
        cid = self.place("x", "--cast", "a,b")
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertNotEqual(st["a"]["room"], st["b"]["room"])
        self.assertNotIn("handed_from", st["b"])
        self.assertNotIn("reset", open(log).read())
        self.cli("close", cid)

    def test_a_plan_in_turns_without_a_reset_hook_is_said_and_each_sets_up(self):
        self.house()
        self.hire_file("a")
        self.hire_file("b")
        p = self.cli("place", "x", "--cast", "a,b", "--rooms", "a>b")
        self.assertIn("no reset hook", p.stderr)
        cid = p.stdout.split()[1]
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertNotEqual(st["a"]["room"], st["b"]["room"])
        self.cli("close", cid)

    def test_a_plan_is_held_to_the_cast(self):
        self.house()
        self.hire_file("a")
        self.hire_file("b")
        self.hire_file("crit", kind="critic")
        for plan, said in (("a+zed", "zed isn't on this contract's cast"), ("a+b, a", "a is in the plan twice"),
                           ("a+crit", "crit is a critic"), ("a++b", "empty name")):
            p = self.cli("place", "x", "--cast", "a,b,crit", "--rooms", plan, check=1)
            self.assertIn(said, p.stderr, plan)

class Sharing(Base):
    """A house whose app takes several users at once shares its rooms: the
    first user sets one up, the rest of the contract's join it."""

    def share_house(self, reset=False):
        self.house()
        log = os.path.join(self.tmp, "setups")
        adm = os.path.join(self.project, ".administration")
        if reset:
            write(os.path.join(adm, "reset"), f'#!/bin/sh\necho "reset $ADMINISTRATION_PROFESSIONAL" >> "{log}"\n', 0o755)
        setup = SETUP.replace("set -e\n", f'set -e\necho "setup $ADMINISTRATION_PROFESSIONAL" >> "{log}"\n', 1)
        setup = setup.replace('"password": "pw-staff"}}}', '"password": "pw-staff"}},\n "logins": {"member": [{"email": "member2@example.com", "password": "pw-member2"}]}}')
        write(os.path.join(adm, "setup"), setup, 0o755)
        return log

    def test_users_share_one_room_each_with_a_login_of_its_own(self):
        log = self.share_house()
        for n in ("a", "b", "c"):
            self.hire_file(n, signs_in_as="member", fake="slow", coins="2m")
        cid = self.place("x", "--cast", "a,b,c", "--rooms", "a+b+c")
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertEqual({s["state"] for s in st.values()}, {"out"}, st)
        # One room set up, by whichever got there first; the others joined it.
        setups = [l for l in open(log).read().split("\n") if l]
        self.assertEqual(len(setups), 1, setups)
        owner = setups[0].split()[1]
        o = st[owner]
        joiners = sorted(set("abc") - {owner})
        for n in joiners:
            self.assertEqual(st[n].get("shared_with"), owner, st)
            self.assertEqual(st[n]["shared_room"], o["room"])
            self.assertNotEqual(st[n]["room"], o["room"])  # a desk and a browser of its own
        # They worked at the same time.
        t = lambda v: time.mktime(time.strptime(v, "%Y-%m-%dT%H:%M:%SZ"))
        for n in joiners:
            self.assertLessEqual(t(st[n]["ready_at"]), t(o["finished_at"]), st)
        # A login each while there were any: the role's own, then the
        # manifest's more; the third shares one, and is told so.
        prompts = {n: self.call(cid, n)["prompt"] for n in "abc"}
        self.assertIn("member@example.com", prompts[owner])
        second = [n for n in joiners if "member2@example.com" in prompts[n]]
        self.assertEqual(len(second), 1, prompts)
        other = (set(joiners) - set(second)).pop()
        self.assertIn("member@example.com", prompts[other])
        self.assertIn("Another tester is signed in with the same login as you", prompts[other])
        self.assertNotIn("Another tester is signed in with the same login", prompts[second[0]])
        for p in prompts.values():
            self.assertIn("Other testers may be using this app at the same time", p)
        # The room was torn down once, as its owner's, by the last out.
        self.assertTrue(os.path.exists(os.path.join(o["room"], owner + ".torn")), os.listdir(o["room"]))
        for n in joiners:
            self.assertFalse(os.path.exists(os.path.join(o["room"], n + ".torn")))
        if t(o["finished_at"]) < min(t(st[n]["finished_at"]) for n in joiners):
            # Done first, it left the room to the others.
            self.assertTrue(o.get("shared_on"), o)
        out = self.cli("status", cid).stdout
        self.assertIn("sharing %s's room" % owner, out)
        self.cli("close", cid)
        self.assertIn("| shared %s's |" % owner, self.cli("report", cid).stdout)
        self.assertFalse(os.path.exists(os.path.join(self.books, "contracts", cid, ".rooms", owner + ".json")))

    def test_together_then_in_turns(self):
        log = self.share_house(reset=True)
        for n in ("a", "b"):
            self.hire_file(n, signs_in_as="member", fake="slow", coins="2m")
        self.hire_file("c", signs_in_as="member")
        # d outlasts a and b, so c's room can only come from them.
        self.hire_file("d", signs_in_as="member", fake="slow", coins="5m")
        # a and b together, then c, put back; d apart. d and a's room fill
        # both rooms, so c waits for a and b's (with a room free, it would
        # set up one of its own rather than wait: in turns is a promise it
        # can wait, not an order to).
        cid = self.place("x", "--cast", "d,a,b,c", "--rooms", "a+b>c", env={"ADMINISTRATION_MAX_ROOMS": "2"})
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertEqual({st[n]["state"] for n in "abcd"}, {"out", "back"}, st)
        lines = [l for l in open(log).read().split("\n") if l]
        self.assertEqual(sorted(l for l in lines if l.startswith("setup")), ["setup a", "setup d"], lines)
        self.assertEqual(len([l for l in lines if l.startswith("reset")]), 1, lines)
        self.assertEqual(st["b"].get("shared_with"), "a", st)
        self.assertIn(st["c"].get("handed_from"), ("a",), st)
        self.assertEqual(st["c"]["room"], st["a"]["room"])
        self.assertNotIn("handed_from", st["d"])
        out = self.cli("status", cid).stdout
        self.assertIn("Room plan: a and b, then c, in turns, together where joined; anyone else in a room of their own.", out)
        self.cli("close", cid)
        report = self.cli("report", cid).stdout
        self.assertIn("Room plan: a and b, then c", report)
        self.assertIn("| a's, put back |", report)

    def test_users_put_apart_never_join(self):
        log = self.share_house()
        for n in ("a", "b"):
            self.hire_file(n, signs_in_as="member", fake="slow", coins="2m")
        cid = self.place("x", "--cast", "a,b")
        self.cli("wait", cid)
        st = self.status(cid)["status"]
        self.assertEqual(sorted(l for l in open(log).read().split("\n") if l), ["setup a", "setup b"])
        self.assertNotIn("shared_with", st["b"])
        self.assertNotIn("Other testers may be using", self.call(cid, "b")["prompt"])
        self.cli("close", cid)

    def test_the_directors_room_plan_is_taken_unless_rooms_is_given(self):
        import re
        self.share_house()
        out = self.cli("director", "cast", "the forms", check=None).stdout
        run = re.search(r"Director (d[0-9-]+-[a-z0-9]{4})", out).group(1)
        rd = os.path.join(self.books, "director", run, "out")
        for n in ("a", "b"):
            write(os.path.join(rd, "candidates", n + ".md"), professional(n, signs_in_as="member", fake="slow", coins="2m"))
        write(os.path.join(rd, "cast.md"), "# Cast\n- a: forms\n- b: forms, read only\nrooms: a+b\n")
        cid = self.place("the forms", "--with", run)
        self.assertIn("Room plan: a and b together", self.cli("status", cid).stdout)
        self.cli("wait", cid)
        self.assertEqual(self.status(cid)["status"]["b"].get("shared_with"), "a")
        self.cli("close", cid)
        # --rooms "" places without it; a plan that doesn't fit the cast says whose it is.
        cid = self.place("the forms", "--with", run, "--rooms", "")
        self.assertNotIn("Room plan:", self.cli("status", cid).stdout)
        self.cli("close", cid)
        write(os.path.join(rd, "cast.md"), "# Cast\nrooms: a+zed\n")
        p = self.cli("place", "the forms", "--with", run, check=1)
        self.assertIn("the Director's rooms: line (a+zed): zed isn't on this contract's cast", p.stderr)

    def test_a_plan_that_fits_isnt_counted_as_a_room_each(self):
        self.share_house()
        for n in ("a", "b", "c"):
            self.hire_file(n, signs_in_as="member")
        # Three users in one room, of two: it fits, and isn't queued.
        p = self.cli("place", "x", "--cast", "a,b,c", "--rooms", "a+b+c", "--no-queue", env={"ADMINISTRATION_MAX_ROOMS": "2"})
        self.assertNotIn("Queued", p.stdout)
        cid = p.stdout.split()[1]
        self.cli("wait", cid)
        self.cli("close", cid)


if __name__ == "__main__":
    unittest.main()
