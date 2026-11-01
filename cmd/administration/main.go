// The Administration: a roster of persona agents that test, and the
// contracts they take. See README.md for the frame and docs/design.md for the plain
// version.
package main

import (
	"fmt"
	"os"
	"strings"
)

// Exit codes are a contract with the skill and with scripts.
const (
	exitOK      = 0
	exitError   = 1 // bad usage, unknown names, a broken file
	exitStart   = 2 // a room or a professional failed to start, or failed at the work (a user whose browser never worked)
	exitRefused = 3 // refused by the Rules
	exitBusy    = 4 // not enough rooms free now: place it again once other contracts' users are back
	exitNewer   = 5 // upgrade --check: a newer release (or upstream commit) is out
)

const usage = `administration: contracts for persona agents.

  administration guide
      how a Claude session uses the Administration (the plugin's skill
      reads it from here, so it always matches this version)
  administration upgrade [--no-browser] | upgrade --check
      install the latest release (with the installer your release came
      with), or pull and rebuild your checkout
      (~/.local/share/the-administration, or $ADMINISTRATION_SRC); open
      sessions use the new version from their next task. --check only
      asks: exit 0 up to date, 5 a newer one is out, 1 couldn't tell
  administration roster [--json] [--project DIR] [--match tag,tag]
      who's hired, where from, and the open rooms; --match is the Director's
      first draft of a cast, and the tags nobody knows
  administration place TARGET... --cast a,b | --with RUN [--coins 10m] [--model M]
                       [--changes [--base REF]] [--brief NAME=TASK]... [--only NAME=TASK]...
                       [--project DIR] [--dry-run] [--show-browsers] [--no-watch] [--no-before] [--data NAME]
      place a contract; prints its id. --changes puts it on the branch's
      changes, and gives users the app before them too, to compare
      (--no-before doesn't). In Herdr or tmux it opens a watch pane per
      professional, unfocused, in your space, each closing when they're
      back; a watch hook opens them instead (--no-watch, or
      ADMINISTRATION_WATCH=off, doesn't); --show-browsers opens users'
      browsers as windows on your screen. --with brings a Director cast's
      specialists; --brief gives one their own task on top of their
      persona's checks, --only instead of them. Exits 4 when the rooms it
      needs are taken (it names who holds them): place it again once
      they're back. Exits 2 when someone fails in its first seconds (3 if
      any was refused); the contract is placed all the same
  administration hire NAME|FILE [--replace] | hire --from CONTRACT NAME
      put a professional the Director trained on the project's roster, or
      keep a one-off specialist from a contract
  administration wait ID [--timeout 3h]
      until every professional is back or out of coins, with a line as each
      one's state changes; returns early (2, or 3 if refused) when one fails
      while others still work
  administration resume ID NAME [--coins 10m]
      a new contract for one professional on what contract ID left undone:
      what they couldn't reach and the steps of their plan never answered,
      or their whole part when they wrote nothing down. Places it as
      place does, so it exits 2, 3 or 4 as place would. A part is resumed
      once: again, it names the contract that picked it up, to resume
      from that one
  administration status [ID] [--json]
      who's working, who's back; with no ID, every open contract and how
      many rooms are free
  administration close ID [--keep]
      stop anyone still working, merge the reports, clear the rooms;
      prints the summary's path
  administration report ID [NAME]
      print the summary (once closed), or one professional's report
  administration room-check [--project DIR] [--data NAME] [--changes [--base REF]]
      build one room with the project's setup hook, check it comes up on
      seed data, show its address and logins, and clear it away
  administration equipment-check NAME [--project DIR] [--shot FILE]
      start one piece of the project's equipment in a fresh room, say what
      its setup printed, take one picture through a screen (saved to FILE,
      or the books), and clear it away
  administration watch ID [NAME] [--panes]
      follow what each professional does as they do it; --panes gives each
      one a pane in Herdr or tmux, or wherever the watch hook puts them
  administration director cast TARGET... [--changes [--base REF]] [--amend RUN] | train WHO... [--contract ID]
                         | onboard [--agents DIR] | review
                         [--project DIR] [--model M] [--coins 10m]
      cast: specialists for one contract, from reading the code in scope;
      place them with --with RUN. --amend RUN starts from an earlier cast,
      keeps what it got right and changes only what's new (equipment it
      asked for, now declared), for a fraction of a new one
      the Director, walled like a professional: writes only into its run folder
  administration director show RUN | apply RUN
      what a run wrote (a cast's plan in full) and the checks it passed;
      apply puts it into .administration/
  administration missed WHAT... [--contract ID] [--by NAME] [--project DIR]
      something a professional saw and passed over; the Director's next
      review tunes whoever should have caught it (apply puts the tuning in)
  administration verify ID [--batch 12 (1 to 50)] [--coins 10m] [--model M]
      check a closed contract's unchecked findings in the house: walled
      verifiers read the project's code (nothing else) and record confirmed,
      likely or false for each, with the line that decided it, and a
      corrected mark where one's wrong (an OLD marked NEW); it also decides
      the links the matcher proposed, also or not_also. Your own verdicts
      replace theirs
  administration verdicts ID FILE|-
      record each finding's check, adding to earlier ones:
      {"name": [{"finding": 1, "verdict": "confirmed|likely|false", "why": "...",
                 "tag": "NEW|CHANGE|OLD|UNMARKED", "also": ["ID NAME #N"], "not_also": ["ID NAME #N"]}]}
      tag corrects a finding's mark and files it there (left out, an
      earlier one stands; UNMARKED for none; a false finding keeps its
      own); also/not_also confirm or reject a link to the same bug in
      another contract
  administration stats [--project DIR] [--json]
      each professional's record: runs, findings, how many held up, coins used
  administration prune [--keep 10] [--older 30d] [--dry-run]
      strip reviewed or old contracts to their summaries; past --keep, to the ledger
  administration check FILE...
      check professional files (what the Director writes)
  administration version

Exit codes: 0 done, 1 error, 2 a room or professional failed (to start,
or a user whose browser never worked), 3 refused by the Rules, 4 not enough
rooms free for it now (place it again once other contracts' users are back),
5 upgrade --check: a newer version is out.
`

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// refusal is an error the Rules raise; it exits 3.
type refusal struct{ msg string }

func (e refusal) Error() string { return e.msg }

// busy is a contract that would fit, but not now: other contracts hold too
// many of the rooms. It exits 4, so a script can wait and place it again.
type busy struct{ msg string }

func (e busy) Error() string { return e.msg }

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return exitOK
	}
	if h, ok := commandHelp(args); ok {
		fmt.Print(h)
		return exitOK
	}
	var err error
	code := exitOK
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "version", "--version":
		fmt.Println("administration", version)
	case "upgrade":
		code, err = cmdUpgrade(args[1:])
	case "guide":
		// The skill's instructions, from the binary they describe: the plugin
		// only points here, so upgrading the tool upgrades them.
		// Where its README is on this machine, so "the README's" section is
		// a file to open, not one to search the disk for.
		readme := readmePath()
		g := strings.NewReplacer("the README's ", "the README's ("+readme+") ", "(README, ", "(the README, "+readme+", ").Replace(guide)
		fmt.Printf("<!-- administration %s: how to use it. Follow this for the task at hand. -->\n\n%s", version, g)
	case "roster":
		err = cmdRoster(args[1:])
	case "place":
		err = cmdPlace(args[1:])
	case "wait":
		code, err = cmdWait(args[1:])
	case "status":
		err = cmdStatus(args[1:])
	case "report":
		err = cmdReport(args[1:])
	case "watch":
		err = cmdWatch(args[1:])
	case "room-check":
		err = cmdRoomCheck(args[1:])
	case "equipment-check":
		err = cmdEquipmentCheck(args[1:])
	case "resume":
		err = cmdResume(args[1:])
	case "verify":
		err = cmdVerify(args[1:])
	case "close":
		err = cmdClose(args[1:])
	case "check":
		err = cmdCheck(args[1:])
	case "hire":
		err = cmdHire(args[1:])
	case "director":
		code, err = cmdDirector(args[1:])
	case "verdicts":
		err = cmdVerdicts(args[1:])
	case "stats":
		err = cmdStats(args[1:])
	case "prune":
		err = cmdPrune(args[1:])
	case "_screen":
		err = cmdScreen(args[1:])
	case "_equip":
		err = cmdEquip(args[1:])
	case "_house":
		// Internal: the record's tools for a professional (record.go).
		err = cmdHouse(args[1:])
	case "missed":
		err = cmdMissed(args[1:])
	case "_drive":
		err = cmdDrive(args[1:])
	case "_gate":
		// Internal: open a gate on the given ports and hold it until stdin
		// closes. For tests/live/gate_probe.py.
		err = gateCmd(args[1:])
	case "_work":
		// Internal: the detached worker for one professional.
		if len(args) != 3 {
			err = usageError{"_work ID NAME"}
		} else {
			err = work(args[1], args[2])
		}
	default:
		err = usageError{fmt.Sprintf("unknown command %q (try --help)", args[0])}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "administration:", err)
		switch err.(type) {
		case refusal:
			return exitRefused
		case startError:
			return exitStart
		case busy:
			return exitBusy
		}
		return exitError
	}
	return code
}

// commandHelp is a command's own entry in the usage, when its first
// argument is --help or -h: agents try that before the README. Only there,
// so a target or a task that happens to be "-h" is still one.
func commandHelp(args []string) (string, bool) {
	if len(args) < 2 || args[1] != "--help" && args[1] != "-h" || strings.HasPrefix(args[0], "_") {
		return "", false
	}
	var b strings.Builder
	in := false
	for _, l := range strings.Split(usage, "\n") {
		if strings.HasPrefix(l, "  administration ") {
			in = strings.HasPrefix(l+" ", "  administration "+args[0]+" ")
		} else if strings.TrimSpace(l) == "" {
			in = false
		}
		if in {
			b.WriteString(strings.TrimPrefix(l, "  ") + "\n")
		}
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// flags is a small parser: --name value, --name=value and bare --switch, with
// everything else positional. Unknown flags are an error.
type flags struct {
	vals  map[string]string
	all   map[string][]string // every value of a flag given more than once
	pos   []string
	known map[string]bool // name -> takes a value
}

func parseFlags(args []string, known map[string]bool) (*flags, error) {
	f := &flags{vals: map[string]string{}, all: map[string][]string{}, known: known}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			f.pos = append(f.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") || a == "-" {
			f.pos = append(f.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		takes, ok := known[name]
		if !ok {
			return nil, usageError{fmt.Sprintf("unknown flag --%s", name)}
		}
		if takes && !hasVal {
			if i+1 >= len(args) {
				return nil, usageError{fmt.Sprintf("--%s needs a value", name)}
			}
			i++
			val = args[i]
		} else if !takes {
			if hasVal {
				return nil, usageError{fmt.Sprintf("--%s takes no value", name)}
			}
			val = "1"
		}
		f.vals[name] = val
		f.all[name] = append(f.all[name], val)
	}
	return f, nil
}

func (f *flags) get(name string) string { return f.vals[name] }
func (f *flags) has(name string) bool   { _, ok := f.vals[name]; return ok }
