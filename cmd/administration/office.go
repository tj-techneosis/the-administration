package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The office: a short label for the machine these books are kept on, for
// people who keep books on more than one and bring their records together.
// A contract id is only unique within one machine's books, so records from
// two offices are joined on (office, id). It's ADMINISTRATION_OFFICE when
// that's set, else the first line of the file "office" in the config
// folder, else none. The Administration never makes one up: no hostname
// goes in the books unless the person put it there.
func officeName() (string, error) {
	if v, ok := os.LookupEnv("ADMINISTRATION_OFFICE"); ok {
		return checkOffice(v, "ADMINISTRATION_OFFICE")
	}
	path := filepath.Join(configDir(), "office")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the office in %s can't be read: %v", path, err)
	}
	return checkOffice(strings.SplitN(string(b), "\n", 2)[0], path)
}

// checkOffice holds a label to a professional's name's shape. Set and
// blank is a mistake, not "no office": it's said, not passed over.
func checkOffice(v, from string) (string, error) {
	// A file saved by an editor that marks its encoding, or ends lines with
	// \r\n, still says the same name.
	v = strings.TrimSpace(strings.TrimPrefix(v, "\ufeff"))
	if v == "" {
		return "", fmt.Errorf("the office in %s is blank: give a short name, or unset it for none", from)
	}
	if !nameRE.MatchString(v) {
		return "", fmt.Errorf("the office in %s is %q: give a short name (lowercase letters, digits and -, up to 64)", from, oneLine(v, 70))
	}
	return v, nil
}

// stampWhere records where a contract is placed: the office and the system
// its rooms run on, as they are now, given the office already read (each
// command reads it before it changes anything). What's recorded is never
// filled in later: a contract placed with no office has none, wherever its
// books go. A resume is stamped like any other contract: it's a new one in
// these books, with an id drawn here, and its rooms run here; the contract
// it picks up from is on its record (resumes).
func (c *contract) stampWhere(office string) {
	c.Office, c.OS = office, runtime.GOOS
}
