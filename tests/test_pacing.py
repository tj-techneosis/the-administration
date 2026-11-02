"""Pacing: the Administration paces its rooms to the machine. One contract
with three users once set up every room at once, two apps each, and the
machine stopped answering for twenty minutes while the seats' coins ran
out waiting. Run through tests/isolated.sh.
"""
import json
import os
import subprocess
import time
import unittest

from test_administration import Base, SETUP, running, write, setUpModule  # noqa: F401

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
        # Each setup takes 1.5s and the limit is 2.5s: the last waits about
        # 3s for its turn, which mustn't count against its limit.
        env = {"ADMINISTRATION_SETUP_TIMEOUT": "2500ms"}
        cid = self.place("x", "--cast", "a,b,c", env=env)
        said = set()
        deadline = time.time() + 30
        while time.time() < deadline:
            st = self.status(cid)["status"]
            said |= {s.get("reason", "") for s in st.values()}
            if all(s["state"] in ("working", "out", "back") for s in st.values()):
                break
            time.sleep(0.1)
        self.assertTrue({"waiting for setup (1 ahead)", "waiting for setup (2 ahead)"} & said, said)
        self.assertIn("setting up", said)
        self.cli("wait", cid)
        # Never two at once.
        spans = {}
        for line in open(log).read().split("\n"):
            if line:
                what, who, t = line.split()
                spans.setdefault(who, {})[what] = float(t)
        self.assertEqual(sorted(spans), ["a", "b", "c"])
        order = sorted(spans.values(), key=lambda s: s["start"])
        for one, two in zip(order, order[1:]):
            self.assertLessEqual(one["end"], two["start"], spans)
        st = self.status(cid)["status"]
        last = max(st.values(), key=lambda s: s.get("waited_secs", 0))
        self.assertGreaterEqual(last["waited_secs"], 2.5, last)
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


if __name__ == "__main__":
    unittest.main()
