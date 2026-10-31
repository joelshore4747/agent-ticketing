"""The tracker's dump, and the counts that describe it (TP-220).

Nothing backed the tracker up before this. compose.yml has one volume, `ticketing-db`, and every
decision, digest and review the roadmap produces lives only there, so these tests are about the two
promises a backup has to keep to be worth having at all.

The first is that the sidecar counts describe the dump, not the database a moment later. The script
ties them together with a single REPEATABLE READ snapshot: psql exports it, pg_dump joins it with
--snapshot, and the counts are read in that same still-open transaction. Note what can and cannot be
tested about that here. On a tracker nobody is writing to, one snapshot and two independent queries
give identical numbers, so `test_the_counts_match_the_database_they_were_dumped_from` would pass
either way and is only a check that the counts are real. The wiring itself is held by
`test_the_dump_and_the_counts_share_one_exported_snapshot`, which reads the id psql exported back off
the pg_dump command line — because the failure being guarded against is invisible on a quiet database
and silent under load, which is the worst possible shape for it: the restore check in TP-222 would
report a mismatch that nobody could tell apart from a write that landed in between.

The second is that copies accumulate under a stated rule instead of one file being overwritten.
BACKUP_KEEP=2 over three runs has to leave exactly the newest two pairs, and a pruned dump has to
take its counts file with it — an orphaned .counts is what would leave a later restore check
comparing a dump against numbers belonging to a dump that no longer exists.

The third is that it happens again without anyone asking (TP-221) — a backup someone has to remember
is not a backup. The schedule is scripts/backup-loop.sh, and `Schedule` runs that very script at
BACKUP_INTERVAL=1: a second dump has to appear on its own, and a dump that fails has to leave the
schedule running, because a database that was restarting for a minute must not stop the only thing
standing between the tracker's volume and a lost history. What cannot be shown without Docker — that
compose.yml's `backup` service runs it against a healthy database, mounts scripts/ read-only and the
backups read-write, and that `just db` starts it while waiting only on `db` — is pinned as text in
`Wiring`, where the failure being guarded against is a dump nobody is taking any more.

The fourth is that a dump can actually be restored (TP-222) — the other three are about a file
appearing on a schedule, and a file is not a backup until something has read it back. scripts/
backup-verify.sh restores into a scratch database and compares its row counts with the sidecar, so
`RestoreCheck` proves both halves of that: a fresh dump comes back with the counts it was written
with, and a counts file that says one ticket more is a FAIL and a non-zero exit rather than a line
nobody reads. Every one of those runs also asserts the two things the check must never get wrong —
the live tracker's counts are unchanged and the scratch database is gone — because a restore test
with a bug in it is a bigger threat to the tracker's history than the missing backup it replaces.
`Schedule` then runs the real loop and waits for two `restore check OK` lines, since a restore test
that only a human runs is the thing this batch set out to stop relying on.

Every run here writes into a temp directory. The real BACKUP_DIR is never touched, and the one test
that does exercise the default points HOME at a temp directory to reach it safely.
"""
import os
import re
import select
import shutil
import subprocess
import tempfile
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "backup.sh")
LOOP = os.path.join(ROOT, "scripts", "backup-loop.sh")
VERIFY = os.path.join(ROOT, "scripts", "backup-verify.sh")
PGURL = os.environ.get("DATABASE_URL", "postgres://ticketing:ticketing@127.0.0.1:5434/ticketing")
TABLES = ("batches", "decisions", "projects", "tickets")
SCRATCH_DB = "ticketing_restore_check"


def slurp(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def run_backup(backup_dir, keep=None, home=None, pghost=None):
    env = {**os.environ, "DATABASE_URL": PGURL}
    for name, value in (("BACKUP_DIR", backup_dir), ("BACKUP_KEEP", keep),
                        ("HOME", home), ("PGHOST", pghost)):
        env.pop(name, None)
        if value is not None:
            env[name] = str(value)
    return subprocess.run(["bash", SCRIPT], capture_output=True, text=True, env=env)


def run_verify(backup_dir, *args):
    """The restore check, against a temp BACKUP_DIR and the host's DATABASE_URL."""
    env = {**os.environ, "DATABASE_URL": PGURL, "BACKUP_DIR": backup_dir}
    env.pop("PGHOST", None)
    return subprocess.run(["bash", VERIFY, *args], capture_output=True, text=True, env=env)


def database_exists(name):
    out = subprocess.run(["psql", PGURL, "-qtAX", "-c",
                          f"SELECT 1 FROM pg_database WHERE datname = '{name}'"],
                         capture_output=True, text=True, check=True)
    return out.stdout.strip() == "1"


def pairs(backup_dir):
    """The basenames of the dumps and of the counts files, each sorted."""
    names = sorted(os.listdir(backup_dir))
    return ([n for n in names if n.endswith(".dump")],
            [n for n in names if n.endswith(".counts")])


def live_counts():
    """`SELECT count(*)` per table, as the counts file spells them."""
    sql = " UNION ALL ".join(f"SELECT '{t}=' || count(*) FROM {t}" for t in TABLES)
    out = subprocess.run(["psql", PGURL, "-qtAX", "-c", f"{sql} ORDER BY 1"],
                         capture_output=True, text=True, check=True)
    return sorted(out.stdout.split())


def compose_block(name):
    """A compose.yml service, with the comment lines directly above it."""
    lines = slurp(os.path.join(ROOT, "compose.yml")).splitlines()
    at = next(n for n, line in enumerate(lines) if line == f"  {name}:")
    top = at
    while top and lines[top - 1].lstrip().startswith("#"):
        top -= 1
    body = []
    for line in lines[at + 1:]:
        if line.strip() and not line.startswith("    "):
            break
        body.append(line)
    return "\n".join(lines[top:at] + body)


def read_until(proc, wanted, count, timeout=90):
    """The schedule's output, read until `count` of its lines carry `wanted`, or time runs out.

    Read rather than slept on, so that a slow machine costs seconds and a broken loop costs the
    timeout, instead of both costing a fixed guess at how long two dumps take.
    """
    log, seen = "", 0
    deadline = time.monotonic() + timeout
    while seen < count:
        left = deadline - time.monotonic()
        if left <= 0 or not select.select([proc.stdout], [], [], left)[0]:
            break
        # Read the pipe itself rather than the buffered stream: select can only see the one.
        chunk = os.read(proc.stdout.fileno(), 4096).decode(errors="replace")
        if not chunk:
            break
        log += chunk
        seen = log.count(wanted)
    return log, seen >= count


def justfile_block(name):
    """A recipe's body plus the comment lines directly above it, which is where a human reads it."""
    lines = slurp(os.path.join(ROOT, "Justfile")).splitlines()
    at = next(n for n, line in enumerate(lines) if re.match(rf"^{name}(\s+[^:]*)?:", line))
    top = at
    while top and lines[top - 1].startswith("#"):
        top -= 1
    body = []
    for line in lines[at + 1:]:
        if line.strip() and not line.startswith((" ", "\t")):
            break
        body.append(line)
    return "\n".join(lines[top:at] + body)


class OneDump(unittest.TestCase):
    """One run of the script, inspected from every side."""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp(prefix="backup-")
        cls.before = live_counts()
        cls.got = run_backup(cls.dir)
        cls.after = live_counts()

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    def setUp(self):
        self.assertEqual(self.got.returncode, 0, self.got.stdout + self.got.stderr)
        self.dumps, self.counts = pairs(self.dir)

    def test_it_writes_one_dump_and_one_counts_file_under_the_same_name(self):
        self.assertEqual(len(self.dumps), 1, self.dumps)
        self.assertEqual([os.path.splitext(n)[0] for n in self.dumps],
                         [os.path.splitext(n)[0] for n in self.counts])

    def test_the_dump_is_named_by_a_utc_timestamp(self):
        self.assertRegex(self.dumps[0], r"^ticketing-\d{8}T\d{6}Z\.dump$")

    def test_the_dump_is_a_custom_format_archive_carrying_the_tickets_table(self):
        # -Fc, and not empty: an archive pg_restore cannot list is not a backup.
        toc = subprocess.run(["pg_restore", "-l", os.path.join(self.dir, self.dumps[0])],
                             capture_output=True, text=True)
        self.assertEqual(toc.returncode, 0, toc.stderr)
        self.assertIn("TABLE DATA public tickets", toc.stdout)

    def test_the_counts_match_the_database_they_were_dumped_from(self):
        self.assertEqual(self.before, self.after, "the tracker was written to mid-test; run it again")
        self.assertEqual(sorted(slurp(os.path.join(self.dir, self.counts[0])).split()), self.before)

    def test_the_counts_name_every_table_the_restore_check_compares(self):
        got = slurp(os.path.join(self.dir, self.counts[0]))
        for table in TABLES:
            self.assertRegex(got, rf"(?m)^{table}=\d+$")


class SharedSnapshot(unittest.TestCase):
    def test_the_dump_and_the_counts_share_one_exported_snapshot(self):
        # The counts are worth keeping only because they describe the rows in the dump beside them.
        # Two independent reads would look right on a quiet tracker, so what is pinned here is the
        # wiring: psql exports a snapshot id and pg_dump is given that same id to join.
        out = tempfile.mkdtemp(prefix="backup-snapshot-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        env = {**os.environ, "DATABASE_URL": PGURL, "BACKUP_DIR": out}
        env.pop("PGHOST", None)
        got = subprocess.run(["bash", "-x", SCRIPT], capture_output=True, text=True, env=env)
        self.assertEqual(got.returncode, 0, got.stderr)

        self.assertIn("pg_export_snapshot()", got.stderr)
        dumped = re.search(r"\bpg_dump\b.*--snapshot=(\S+)", got.stderr)
        self.assertIsNotNone(dumped, "pg_dump ran without joining the exported snapshot")
        # Postgres spells an exported snapshot id like 00000004-0000001B-1.
        self.assertRegex(dumped.group(1), r"^[0-9A-F]{8}-[0-9A-F]{8}-\d+$")


class TwoCallers(unittest.TestCase):
    def test_pghost_decides_the_connection_instead_of_database_url(self):
        # The backup service of TP-221 connects with PGHOST=db and no DATABASE_URL at all. If the
        # script passed its conninfo regardless, that service would quietly dump whatever
        # DATABASE_URL happened to name — here, the live tracker — and look like it worked.
        out = tempfile.mkdtemp(prefix="backup-pghost-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        got = run_backup(out, pghost=os.path.join(out, "no-such-socket-dir"))
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertEqual(pairs(out), ([], []), "it dumped somewhere PGHOST did not point")


class Retention(unittest.TestCase):
    def test_three_runs_keeping_two_leave_the_newest_two_pairs(self):
        out = tempfile.mkdtemp(prefix="backup-keep-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        made = []
        for _ in range(3):
            got = run_backup(out, keep=2)
            self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
            made.append(re.search(r"wrote (\S+\.dump)", got.stdout).group(1))

        dumps, counts = pairs(out)
        self.assertEqual(len(dumps), 2, f"kept {dumps} on BACKUP_KEEP=2")
        self.assertEqual(len(counts), 2, f"a pruned dump left its counts file behind: {counts}")
        self.assertFalse(os.path.exists(made[0]), "the oldest dump survived the prune")
        self.assertFalse(os.path.exists(f"{os.path.splitext(made[0])[0]}.counts"))
        for kept in made[1:]:
            self.assertTrue(os.path.exists(kept), f"{kept} was pruned instead of the oldest")


class Schedule(unittest.TestCase):
    """scripts/backup-loop.sh: the next dump happens with nobody asking for it."""

    def start(self, **overrides):
        """The loop, dumping into a temp directory once a second instead of once every six hours."""
        out = tempfile.mkdtemp(prefix="backup-loop-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        env = {**os.environ, "DATABASE_URL": PGURL, "BACKUP_DIR": out, "BACKUP_INTERVAL": "1"}
        env.pop("PGHOST", None)  # here the connection is DATABASE_URL's; in the service it is PGHOST
        env.update(overrides)
        proc = subprocess.Popen(["bash", LOOP], env=env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.addCleanup(proc.kill)
        return out, proc

    def stop(self, proc):
        """The end of the schedule, as `just stop` ends it: SIGTERM, and it goes quietly."""
        proc.terminate()
        rest = proc.communicate(timeout=60)[0]
        self.assertEqual(proc.returncode, 0,
                         f"SIGTERM left the loop exiting {proc.returncode}, so `just stop` waits "
                         f"out Docker's grace period and kills a dump mid-write:\n{rest}")
        return rest

    def test_a_second_dump_appears_after_the_interval_with_nobody_asking(self):
        out, proc = self.start()
        log, twice = read_until(proc, "wrote ", 2)
        log += self.stop(proc)
        self.assertTrue(twice, f"one dump and then nothing:\n{log}")

        dumps, counts = pairs(out)
        self.assertGreaterEqual(len(dumps), 2, log)
        self.assertEqual([os.path.splitext(n)[0] for n in dumps],
                         [os.path.splitext(n)[0] for n in counts], "a dump lost its counts file")
        for name in dumps[:2]:
            self.assertIn(name, log, "the log does not name the dump it wrote")
        self.assertRegex(log, r"next backup at \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \(in 1s\)")

    def test_every_scheduled_dump_is_proved_by_a_restore_check(self):
        # The half of TP-222 that a human running `just backup-verify` cannot stand in for: the
        # schedule restores what it has just dumped, so an unrestorable dump is found by the log
        # rather than by whoever eventually needs it back.
        out, proc = self.start()
        log, twice = read_until(proc, "restore check OK", 2)
        log += self.stop(proc)
        self.assertTrue(twice, f"the schedule dumped without proving the dump came back:\n{log}")
        self.assertRegex(log, r"restored \S+\.dump: tickets \d+=\d+.*: OK")
        self.assertFalse(database_exists(SCRATCH_DB), f"the schedule left its scratch database:\n{log}")

        # Each check names the dump written just above it. BACKUP_DIR is shared with the host, so a
        # check that took whatever was newest could say OK about a dump this run never wrote — and,
        # the host's client tools not being the service's, one it could not even have read.
        wrote = re.findall(r"wrote (\S+\.dump)", log)
        checked = re.findall(r"restored (\S+\.dump):", log)
        self.assertEqual(checked[:2], wrote[:2], f"a check named a dump its own run did not write:\n{log}")

    def test_a_failed_dump_does_not_end_the_schedule(self):
        # The database restarts, or the disk is full for a minute. If one bad dump ended the
        # schedule, the service would sit there running and backing nothing up until someone
        # thought to look — which is the state this whole batch exists to get out of.
        out, proc = self.start(PGHOST=os.path.join(tempfile.gettempdir(), "no-such-socket-dir"))
        log, twice = read_until(proc, "next backup at", 2)
        log += self.stop(proc)
        self.assertTrue(twice, f"the schedule stopped at the first failed dump:\n{log}")
        self.assertGreaterEqual(log.count("backup failed"), 2, log)
        self.assertEqual(pairs(out), ([], []), "it dumped somewhere PGHOST did not point")

    def test_an_interval_that_is_not_a_number_stops_before_it_can_hot_loop(self):
        # `sleep abc` fails instantly, so an unchecked interval is not a slow schedule but no
        # schedule at all: a dump as fast as the disk allows, churning fourteen files for ever.
        out, proc = self.start(BACKUP_INTERVAL="abc")
        log = proc.communicate(timeout=60)[0]
        self.assertEqual(proc.returncode, 2, log)
        self.assertIn("BACKUP_INTERVAL", log)
        self.assertEqual(pairs(out), ([], []), "it dumped before checking it could schedule another")


class RestoreCheck(unittest.TestCase):
    """scripts/backup-verify.sh: a dump nobody has restored is a hope, not a backup (TP-222)."""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp(prefix="backup-verify-")
        got = run_backup(cls.dir)
        assert got.returncode == 0, got.stdout + got.stderr
        cls.dump = re.search(r"wrote (\S+\.dump)", got.stdout).group(1)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    def check(self, backup_dir, *args):
        """One run, with the two things it must never get wrong asserted around every single one."""
        before = live_counts()
        got = run_verify(backup_dir, *args)
        self.assertEqual(live_counts(), before,
                         "the restore check wrote to the live tracker — the one database in this "
                         f"repo whose history nothing else can rebuild:\n{got.stdout}{got.stderr}")
        self.assertFalse(database_exists(SCRATCH_DB),
                         f"{SCRATCH_DB} outlived the check, so the next run begins by force-dropping "
                         f"a database that may be in use:\n{got.stdout}{got.stderr}")
        return got

    def test_a_fresh_dump_comes_back_whole(self):
        got = self.check(self.dir)
        line = got.stdout.strip()
        self.assertEqual(got.returncode, 0, line + got.stderr)
        self.assertRegex(line, r"^restored \S+\.dump: tickets \d+=\d+, decisions \d+=\d+, "
                               r"batches \d+=\d+, projects \d+=\d+: OK$")
        self.assertIn(self.dump, line)
        for restored, dumped in re.findall(r"(\d+)=(\d+)", line):
            self.assertEqual(restored, dumped, line)

    def test_a_counts_file_that_does_not_describe_the_dump_is_a_fail(self):
        # The pair the whole check turns on. The dump is untouched and its counts file is edited to
        # claim one ticket more than it holds, so the restored side has to report what it actually
        # restored. If it echoed the file instead, this would pass while a dump that came back short
        # passed too — which is the only failure a restore test exists to catch.
        out = tempfile.mkdtemp(prefix="backup-tamper-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        for name in os.listdir(self.dir):
            shutil.copy(os.path.join(self.dir, name), out)
        counts = os.path.join(out, pairs(out)[1][0])
        real = dict(line.split("=") for line in slurp(counts).split())
        with open(counts, "w", encoding="utf-8") as f:
            for table in sorted(real):
                claimed = int(real[table]) + 1 if table == "tickets" else real[table]
                f.write(f"{table}={claimed}\n")

        got = self.check(out)
        line = got.stdout.strip()
        self.assertNotEqual(got.returncode, 0, f"a mismatch exited 0:\n{line}")
        self.assertTrue(line.endswith(": FAIL"), line)
        self.assertIn(f"tickets {real['tickets']}={int(real['tickets']) + 1}", line)
        # The three tables nobody edited still agree, so the FAIL line names what actually differs
        # rather than sending a human through four tables to find the one that moved.
        for table in ("decisions", "batches", "projects"):
            self.assertIn(f"{table} {real[table]}={real[table]}", line)

    def test_it_checks_the_newest_dump_unless_you_name_one(self):
        out = tempfile.mkdtemp(prefix="backup-newest-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        made = [re.search(r"wrote (\S+\.dump)", run_backup(out).stdout).group(1) for _ in range(2)]
        self.assertIn(made[1], self.check(out).stdout, "it checked a dump that is not the newest")
        self.assertIn(made[0], self.check(out, made[0]).stdout, "it ignored the dump it was given")

    def test_a_dump_pg_restore_cannot_read_is_a_fail(self):
        # The other way a restore fails, and the one the counts cannot speak for: nothing comes back
        # at all. It matters that this is still a FAIL line and a non-zero exit, because the script
        # deliberately does not take pg_restore's exit status as its verdict — the host's client
        # tools are newer than the server and every dump arrives with one ignored error already.
        out = tempfile.mkdtemp(prefix="backup-corrupt-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        for name in os.listdir(self.dir):
            shutil.copy(os.path.join(self.dir, name), out)
        with open(os.path.join(out, pairs(out)[0][0]), "wb") as f:
            f.write(b"this is not a pg_dump archive")

        got = self.check(out)
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertIn("pg_restore could not finish: FAIL", got.stdout)

    def test_a_dump_whose_counts_file_is_missing_is_refused_rather_than_guessed_at(self):
        # The prune in backup.sh deletes a dump and its counts file together, so a lone dump means
        # something went wrong. Checking it against nothing would print an OK that means nothing.
        out = tempfile.mkdtemp(prefix="backup-lonely-")
        self.addCleanup(shutil.rmtree, out, ignore_errors=True)
        shutil.copy(self.dump, out)
        got = self.check(out)
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertIn("counts", got.stderr)
        self.assertNotIn("OK", got.stdout)


class DefaultDirectory(unittest.TestCase):
    def test_the_default_is_under_home_and_outside_the_checkout(self):
        home = tempfile.mkdtemp(prefix="backup-home-")
        self.addCleanup(shutil.rmtree, home, ignore_errors=True)
        got = run_backup(None, home=home)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertEqual(len(pairs(os.path.join(home, ".local/share/ticketing/backups"))[0]), 1)

    def test_no_dump_ever_lands_in_the_repository(self):
        # A dump committed by accident is the whole tracker, passwords and all, in git history.
        dirty = subprocess.run(["git", "-C", ROOT, "status", "--porcelain"],
                               capture_output=True, text=True, check=True).stdout
        self.assertNotIn(".dump", dirty)
        self.assertNotIn(".counts", dirty)


class Wiring(unittest.TestCase):
    def test_just_backup_runs_the_script(self):
        self.assertIn("./scripts/backup.sh", justfile_block("backup"))

    def test_just_backup_verify_runs_the_script_and_passes_a_named_dump_through(self):
        block = justfile_block("backup-verify")
        self.assertIn("./scripts/backup-verify.sh", block)
        self.assertIn("{{DUMP}}", block)

    def test_just_test_runs_this_file(self):
        self.assertIn("scripts/test_backup.py", justfile_block("test"))

    def test_the_justfile_states_the_retention_rule_where_the_human_reads_it(self):
        # The ticket asks for it in the script header and the Justfile comment, and nowhere else.
        block = justfile_block("backup")
        self.assertIn("BACKUP_KEEP", block)
        self.assertIn("14", block)

    def test_the_backup_service_runs_the_schedule_against_a_healthy_database(self):
        block = compose_block("backup")
        self.assertIn("image: postgres:16-alpine", block)
        self.assertIn("condition: service_healthy", block)
        self.assertIn("PGHOST: db", block)
        self.assertIn("backup-loop.sh", block)

    def test_the_scripts_are_mounted_read_only_and_the_backups_are_not(self):
        # A service that could write to scripts/ could rewrite the script that dumps the database;
        # a BACKUP_DIR mounted read-only would give a service that ran on time and saved nothing.
        block = compose_block("backup")
        self.assertIn("./scripts:/scripts:ro", block)
        self.assertRegex(block, r"(?m)^\s+- \$\{BACKUP_DIR:-\S+\}:/backups$")
        self.assertIn("BACKUP_DIR: /backups", block)

    def test_nothing_about_the_backup_service_can_hold_just_db_up(self):
        # `just db` gates dev, test and loop. A healthcheck here, or a --wait naming this service,
        # would put a dump in progress in front of every one of them.
        self.assertNotRegex(compose_block("backup"), r"(?m)^\s+healthcheck:")
        block = justfile_block("db")
        self.assertIn("up -d backup", block)
        waits = [line for line in block.splitlines()
                 if "--wait" in line and not line.strip().startswith("#")]
        self.assertTrue(waits, block)
        for line in waits:
            self.assertNotIn("backup", line, "just db waits on the backup service")

    def test_just_db_creates_the_backup_directory_before_docker_mounts_it(self):
        # Docker creates a missing bind source as root, which would leave `just backup` on the host
        # unable to write into its own backup directory on every fresh checkout.
        self.assertRegex(justfile_block("db"), r"(?m)^\s+mkdir -p .*BACKUP_DIR")

    def test_just_stop_stops_the_scheduled_backup_too(self):
        self.assertRegex(justfile_block("stop"), r"(?m)^\s+docker compose stop .*\bbackup\b")

    def test_the_interval_default_is_six_hours_and_is_stated_once(self):
        self.assertIn("${BACKUP_INTERVAL:-21600}", slurp(LOOP))
        self.assertNotIn("21600", slurp(os.path.join(ROOT, "compose.yml")))

    def test_the_default_backup_directory_is_spelled_the_same_everywhere_it_is_stated(self):
        # backup.sh states it to run on its own, compose.yml to mount it without just, and the
        # Justfile to create it before Docker does. Three statements of one path have to agree.
        for path in ("scripts/backup.sh", "compose.yml", "Justfile"):
            self.assertIn(".local/share/ticketing/backups", slurp(os.path.join(ROOT, path)), path)


if __name__ == "__main__":
    unittest.main()
