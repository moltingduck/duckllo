// Package dotenv loads KEY=VALUE pairs from a file into os.Environ.
//
// Intentionally small — supports `KEY=value`, `KEY="quoted value"`,
// `KEY='single quoted'`, and `# line comments`. No interpolation, no
// multi-line values. That's enough for the duckllo runner's needs and
// keeps us off third-party deps.
//
// Existing env vars win: if KEY is already set in the process env, the
// file value is *not* applied. This makes CLI/CI overrides natural.
package dotenv

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LoadDefault tries each candidate filename in order and returns the path
// it loaded, or "" if none existed. Errors other than "file not found"
// are returned. Defaults are .duckllo.env then .env.
func LoadDefault() (string, error) {
	for _, name := range []string{".duckllo.env", ".env"} {
		ok, err := loadIfExists(name)
		if err != nil {
			return "", err
		}
		if ok {
			abs, _ := filepath.Abs(name)
			return abs, nil
		}
	}
	return "", nil
}

// Load reads a single dotenv file. Missing files are an error; use
// loadIfExists for soft loads.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return parse(f)
}

func loadIfExists(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return true, parse(f)
}

func parse(r io.Reader) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Allow `export KEY=value` for ergonomics — common in shell-style
		// dotenvs that humans paste from documentation snippets.
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue // malformed line; skip rather than abort the whole file
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Strip a single layer of matching surrounding quotes.
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}

		// Process env wins. Don't clobber an explicit override.
		if _, present := os.LookupEnv(key); present {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}
