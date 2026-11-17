"""Kept rooms: one room kept up on a commit, with nobody in it, for people to
look at the app. It waits in the same line for rooms as the contracts, so
it can never take a room from one, and it goes when it's closed or its time
runs out. Run through tests/isolated.sh.
"""
import json
import os
import subprocess
import time
import unittest
import urllib.request

from test_administration import Base, write, setUpModule  # noqa: F401


class Kept(Base):
    def setUp(self):
        super().setUp()
        self.house()
        self.count = os.path.join(self.tmp, "builds")
        # A build hook, so a kept room's build is the one contracts on the
        # same commit install, and the count says how many were made.
        write(os.path.join(self.project, ".administration", "build"),
              f'#!/bin/sh\necho built >> "{self.count}"\necho app > "$2/app"\n', 0o755)
        # Teardown says so outside the room, whose folder goes with it.
        self.torn = os.path.join(self.tmp, "torn")
        write(os.path.join(self.project, ".administration", "teardown"),
              f'#!/bin/sh\necho "$ADMINISTRATION_CONTRACT" >> "{self.torn}"\n', 0o755)
        write(os.path.join(self.project, "app.txt"), "one\n")
        self.git("add", "-A")
        self.git("commit", "-qm", "one")
        self.head = self.git("rev-parse", "HEAD")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.project, check=True, capture_output=True, text=True,
                              env={**os.environ, **self.env}).stdout.strip()

    def keep(self, *args, check=0, env=None):
        p = self.cli("room-keep", *args, "--json", check=check, env=env)
        return json.loads(p.stdout) if check == 0 else p

    def rooms(self):
        return {r["id"]: r for r in json.loads(self.cli("rooms", "--json").stdout)}

    def wait_kept(self, rid, states, within=20):
        deadline = time.time() + within
        while time.time() < deadline:
            r = self.rooms().get(rid)
            if r and r["state"] in states:
                return r
            time.sleep(0.1)
        self.fail(f"kept room {rid} never reached {states}: {self.rooms()}")

    def gone(self, rid, within=20):
        deadline = time.time() + within
        while time.time() < deadline:
            if not os.path.exists(os.path.join(self.books, "contracts", rid)):
                return
            time.sleep(0.1)
        self.fail(f"kept room {rid} still in the books: {self.rooms()}")

    def answers(self, url):
        try:
            urllib.request.urlopen(url, timeout=2).read()
            return True
        except OSError:
            return False

    def test_a_kept_room_comes_up_reports_its_addresses_and_closes_clean(self):
        r = self.keep(self.head)
        self.assertEqual((r["commit"], r["ref"], r["hours"]), (self.head, self.head, 8))
        up = self.wait_kept(r["id"], {"up"})
        self.assertTrue(up["url"].startswith("http://127.0.0.1:"), up)
        self.assertTrue(self.answers(up["url"]), up)
        self.assertIn("web", up["ports"])
        self.assertEqual(up["url"], f"http://127.0.0.1:{up['ports']['web']}/")
        self.assertEqual(up["logins"]["member"]["email"], "member@example.com")
        self.assertGreater(up["left_secs"], 7 * 3600)
        room = self.status(r["id"])["status"]["room"]["room"]
        # Set up as a contract's room: the setup hook, given the build, and
        # told it's a kept room.
        env = open(os.path.join(room, "app", "setup-env")).read()
        self.assertIn("ADMINISTRATION_KEPT=1", env)
        self.assertIn("ADMINISTRATION_BUILD_COMMIT=" + self.head, env)
        self.assertNotIn("do-not-leak", env)
        # Status shows it, with who placed it and its time.
        out = self.cli("status").stdout
        self.assertRegex(out, rf"Kept room {r['id']} at {self.head[:7]}\w*, placed \w+ \d\d:\d\d by t: up at {up['url']}, 7h59m\d+s left")
        self.assertIn("Rooms: 1 of 4 in use, 3 free.", out)
        self.assertIn(f"Kept room {r['id']}", self.cli("rooms").stdout)
        # Not a contract.
        self.assertIn("is a kept room, not a contract", self.cli("wait", r["id"], check=1).stderr)
        out = self.cli("room-close", r["id"]).stdout
        self.assertIn(f"Kept room {r['id']} closed and torn down.", out)
        self.assertEqual(open(self.torn).read(), r["id"] + "\n")
        self.assertFalse(self.answers(up["url"]))
        self.assertFalse(os.path.exists(room))
        self.assertEqual(self.rooms(), {})
        self.assertEqual(self.rooms_left(), [])
        self.assertFalse(os.path.exists(os.path.join(self.books, "contracts", r["id"])))
        self.assertIn("No open contracts. Rooms: 0 of 4 in use, 4 free.", self.cli("status").stdout)
        self.assertIn("no kept room", self.cli("room-close", r["id"], check=1).stderr)

    def test_its_time_runs_out_and_it_tears_down_and_leaves(self):
        # 3.6 seconds, then extended by 3.6 more.
        r = self.keep(self.head, "--hours", "0.001")
        up = self.wait_kept(r["id"], {"up"})
        t0 = time.time()
        ext = json.loads(self.cli("room-extend", r["id"], "--hours", "0.001", "--json").stdout)
        self.assertEqual(ext["hours"], 0.002)
        room = self.status(r["id"])["status"]["room"]["room"]
        self.gone(r["id"])
        self.assertGreater(time.time() - t0, 3, "the extension was ignored")
        self.assertEqual(open(self.torn).read(), r["id"] + "\n")
        self.assertFalse(self.answers(up["url"]))
        self.assertFalse(os.path.exists(room))
        self.assertEqual(self.rooms_left(), [])
        self.assertIn("No kept rooms. Rooms: 0 of 4 in use, 4 free.", self.cli("rooms").stdout)

    def test_a_kept_room_waits_its_turn_when_every_room_is_taken(self):
        self.env["ADMINISTRATION_MAX_ROOMS"] = "1"
        self.hire_file("slow", fake="slow", coins="30m")
        cid = self.place("x", "--cast", "slow")
        self.wait_state(cid, "slow", {"working"})
        p = self.cli("room-keep", self.head)
        rid = p.stdout.split()[2]
        self.assertIn(f"Queued, 1 ahead: 1 of 1 rooms are in use (contract {cid}, placed by t).", p.stdout)
        self.assertEqual(self.wait_kept(rid, {"waiting"})["reason"], "waiting for a room (all 1 in use)")
        self.assertIn("waiting for a room (all 1 in use)", self.cli("status").stdout)
        # --no-queue refuses it instead.
        self.assertIn("1 of 1 rooms are in use and 1 user is waiting for one", self.keep(self.head, "--no-queue", check=4).stderr)
        time.sleep(1)
        self.assertEqual(self.rooms()[rid]["state"], "waiting")
        # The contract's room frees: the kept room takes it by itself.
        self.cli("close", cid)
        up = self.wait_kept(rid, {"up"})
        self.assertTrue(self.answers(up["url"]))
        self.assertGreater(up["waited_secs"], 0)
        self.cli("room-close", rid)

    def test_a_contract_placed_while_a_kept_room_is_up_queues_behind_it(self):
        self.env["ADMINISTRATION_MAX_ROOMS"] = "1"
        self.hire_file("member")
        rid = self.keep(self.head)["id"]
        up = self.wait_kept(rid, {"up"})
        # The same commit's build, made once: the contract will install it.
        self.assertEqual(open(self.count).read(), "built\n")
        p = self.cli("place", "x", "--cast", "member")
        cid = p.stdout.split()[1]
        self.assertIn(f"Queued, 1 ahead: 1 of 1 rooms are in use (contract {rid}, placed by t).", p.stdout)
        self.assertEqual(self.wait_state(cid, "member", {"waiting"})["reason"], "waiting for a room (all 1 in use)")
        time.sleep(1)
        # The kept room is untouched by it.
        self.assertEqual(self.rooms()[rid]["state"], "up")
        self.assertTrue(self.answers(up["url"]))
        self.cli("room-close", rid)
        self.cli("wait", cid)
        self.assertEqual(self.status(cid)["status"]["member"]["state"], "back")
        self.assertEqual(open(self.count).read(), "built\n")
        self.cli("close", cid)

    def test_close_on_a_kept_room_closes_it_too(self):
        rid = self.keep(self.head)["id"]
        self.wait_kept(rid, {"up"})
        self.assertIn(f"Kept room {rid} closed and torn down.", self.cli("close", rid).stdout)
        self.assertEqual(self.rooms(), {})

    def test_closing_a_kept_room_while_it_waits_takes_it_out_of_line(self):
        self.env["ADMINISTRATION_MAX_ROOMS"] = "1"
        first = self.keep(self.head)["id"]
        self.wait_kept(first, {"up"})
        second = self.keep(self.head)["id"]
        self.wait_kept(second, {"waiting"})
        self.cli("room-close", second)
        self.assertEqual(list(self.rooms()), [first])
        self.assertIn("Rooms: 1 of 1 in use, 0 free.", self.cli("status").stdout)
        self.cli("room-close", first)

    def test_a_setup_that_fails_frees_its_room_and_says_why(self):
        write(os.path.join(self.project, ".administration", "setup"), "#!/bin/sh\necho no database >&2\nexit 1\n", 0o755)
        rid = self.keep(self.head)["id"]
        r = self.wait_kept(rid, {"failed"})
        self.assertIn("setup failed", r["reason"])
        self.assertIn("no database", open(r["setup_log"]).read())
        self.assertIn("Rooms: 0 of 4 in use", self.cli("status").stdout)
        self.cli("room-close", rid)
        self.assertEqual(self.rooms(), {})

    def test_refused_up_front(self):
        # A commit this checkout doesn't have.
        self.assertIn("doesn't have that commit", self.keep("0" * 40, check=1).stderr)
        self.assertIn("not a commit here", self.keep("no-such-branch", check=1).stderr)
        self.assertIn("--hours", self.keep(self.head, "--hours", "0", check=1).stderr)
        self.assertIn("at most 72 hours", self.keep(self.head, "--hours", "100", check=1).stderr)
        # Without a build hook the setup builds from the checkout: it must
        # be that commit, with no uncommitted work.
        os.remove(os.path.join(self.project, ".administration", "build"))
        first = self.head
        write(os.path.join(self.project, "app.txt"), "two\n")
        self.git("commit", "-qam", "two")
        self.assertIn("check out " + first[:7], self.keep(first, check=1).stderr)
        write(os.path.join(self.project, "app.txt"), "wip\n")
        self.assertIn("uncommitted work", self.keep("HEAD", check=1).stderr)
        self.assertEqual(self.glob_contracts(), [])
        # A contract's id isn't a kept room's.
        self.git("checkout", "-q", "app.txt")
        self.hire_file("member")
        cid = self.place("x", "--cast", "member")
        self.assertIn("is a contract, not a kept room", self.cli("room-close", cid, check=1).stderr)
        self.cli("wait", cid)
        self.cli("close", cid)


if __name__ == "__main__":
    unittest.main()
