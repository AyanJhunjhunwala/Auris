package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads the first .env file found in paths into the process
// environment. Variables already set in the real environment win, so
// `FOO=bar make dev` still overrides the file. Returns the file used ("" if
// none). Supports KEY=VALUE, optional `export `, quotes and # comments.
func LoadDotEnv(paths ...string) string {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			} else if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i]) // trailing comment on an unquoted value
			}
			if _, set := os.LookupEnv(k); !set && k != "" {
				os.Setenv(k, v)
			}
		}
		f.Close()
		return p
	}
	return ""
}
