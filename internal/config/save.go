package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// SaveSet updates only the named set and, when requested, the default-set key.
// Untouched TOML bytes (including comments and unknown application settings)
// remain intact. An optimistic digest check detects edits since Load.
func (c Config) SaveSet(name string, ids []string, makeDefault bool) (Config, error) {
	if !c.loaded || c.Path == "" {
		return c, errors.New("load configuration before saving a manager set")
	}
	if !setName.MatchString(name) {
		return c, fmt.Errorf("invalid manager set name %q", name)
	}
	normalized, err := normalizeIDs(ids)
	if err != nil {
		return c, err
	}
	if len(normalized) == 0 {
		return c, errors.New("a manager set must contain at least one manager")
	}
	if err := os.MkdirAll(filepath.Dir(c.resolvedPath), 0700); err != nil {
		return c, err
	}
	lockPath := c.resolvedPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return c, fmt.Errorf("configuration is being saved or its lock is unavailable: %w", err)
	}
	lock.Close()
	defer os.Remove(lockPath)
	original, mode, err := c.currentSource()
	if err != nil {
		return c, err
	}
	literal, _ := json.Marshal(normalized)
	updated, err := setValue(original, []string{"manager_sets", name}, literal)
	if err != nil {
		return c, err
	}
	if makeDefault {
		value, _ := json.Marshal(name)
		updated, err = setValue(updated, []string{"default_manager_set"}, value)
		if err != nil {
			return c, err
		}
	}
	var check Config
	if err := toml.Unmarshal(updated, &check); err != nil {
		return c, fmt.Errorf("cannot safely update this TOML layout: %w", err)
	}
	if err := check.validate(); err != nil {
		return c, err
	}
	f, err := os.CreateTemp(filepath.Dir(c.resolvedPath), ".lazypkg-config-*")
	if err != nil {
		return c, err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(updated)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return c, err
	}
	// Recheck immediately before publishing as external editors need not honor
	// lazypkg's cooperative lock.
	if _, _, err := c.currentSource(); err != nil {
		return c, err
	}
	if err := os.Rename(temp, c.resolvedPath); err != nil {
		return c, fmt.Errorf("publish configuration: %w", err)
	}
	next := c
	next.ManagerSets = map[string][]string{}
	for k, v := range c.ManagerSets {
		next.ManagerSets[k] = append([]string(nil), v...)
	}
	next.ManagerSets[name] = normalized
	if makeDefault {
		next.DefaultManagerSet = name
	}
	next.sourceDigest = sha256.Sum256(updated)
	next.sourceExists = true
	if resolved, err := filepath.EvalSymlinks(c.Path); err == nil {
		next.resolvedPath, _ = filepath.Abs(resolved)
	}
	return next, nil
}

func (c Config) currentSource() ([]byte, os.FileMode, error) {
	changed := func() error { return errors.New("configuration changed since it was loaded; reload before saving") }
	b, err := os.ReadFile(c.Path)
	if os.IsNotExist(err) && !c.sourceExists {
		return nil, 0600, nil
	}
	if os.IsNotExist(err) && c.sourceExists {
		return nil, 0, changed()
	}
	if err != nil {
		return nil, 0, err
	}
	if !c.sourceExists || sha256.Sum256(b) != c.sourceDigest {
		return nil, 0, changed()
	}
	resolved, err := filepath.EvalSymlinks(c.Path)
	if err != nil {
		return nil, 0, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, 0, err
	}
	if resolved != c.resolvedPath {
		return nil, 0, changed()
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("configuration is not a regular file")
	}
	return b, info.Mode().Perm(), nil
}

type valueSpan struct{ start, end int }
type tomlIndex struct {
	values     map[string]valueSpan
	tables     map[string]int
	firstTable int
}

func pathKey(path []string) string { return strings.Join(path, "\x00") }
func nodeKeys(n *unstable.Node) ([]string, int) {
	keys := []string{}
	end := 0
	it := n.Key()
	for it.Next() {
		k := it.Node()
		keys = append(keys, string(k.Data))
		end = int(k.Raw.Offset + k.Raw.Length)
	}
	return keys, end
}

func indexTOML(data []byte) (tomlIndex, error) {
	index := tomlIndex{values: map[string]valueSpan{}, tables: map[string]int{}, firstTable: len(data)}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	current := []string{}
	tableKey := ""
	var visit func(*unstable.Node, []string) error
	visit = func(n *unstable.Node, prefix []string) error {
		keys, keyEnd := nodeKeys(n)
		full := append(append([]string(nil), prefix...), keys...)
		start := keyEnd
		for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		if start >= len(data) || data[start] != '=' {
			return errors.New("could not locate TOML value")
		}
		start++
		for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		end, err := scanValue(data, start)
		if err != nil {
			return err
		}
		index.values[pathKey(full)] = valueSpan{start, end}
		if n.Value().Kind == unstable.InlineTable {
			children := n.Value().Children()
			for children.Next() {
				child := children.Node()
				if child.Kind == unstable.KeyValue {
					if err := visit(child, full); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for parser.NextExpression() {
		n := parser.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			keys, _ := nodeKeys(n)
			first := n.Key()
			first.Next()
			offset := int(first.Node().Raw.Offset)
			line := bytes.LastIndexByte(data[:offset], '\n') + 1
			if line < index.firstTable {
				index.firstTable = line
			}
			if tableKey != "" {
				index.tables[tableKey] = line
			}
			current = keys
			tableKey = pathKey(keys)
			index.tables[tableKey] = len(data)
		case unstable.KeyValue:
			if err := visit(n, current); err != nil {
				return index, err
			}
		}
	}
	return index, parser.Error()
}

// scanValue locates a value's lexical end while retaining its trailing comment.
// Decoding has already validated the TOML; this scanner only establishes spans.
func scanValue(data []byte, start int) (int, error) {
	depth := 0
	quote := byte(0)
	triple := false
	escaped := false
	for i := start; i < len(data); i++ {
		c := data[i]
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
				continue
			}
			if quote == '"' && c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				if triple {
					if i+2 < len(data) && data[i+1] == quote && data[i+2] == quote {
						i += 2
						quote = 0
					}
				} else {
					quote = 0
				}
			}
			if quote == 0 && depth == 0 {
				return i + 1, nil
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			triple = i+2 < len(data) && data[i+1] == c && data[i+2] == c
			if triple {
				i += 2
			}
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
			if depth < 0 {
				return i, nil
			}
		case '#':
			if depth == 0 {
				return trimEnd(data, start, i), nil
			}
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case '\n', '\r', ',':
			if depth == 0 {
				return trimEnd(data, start, i), nil
			}
		}
	}
	if depth != 0 || quote != 0 {
		return 0, errors.New("unterminated TOML value")
	}
	return trimEnd(data, start, len(data)), nil
}
func trimEnd(data []byte, start, end int) int {
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t') {
		end--
	}
	return end
}
func replaceBytes(data []byte, start, end int, value []byte) []byte {
	out := make([]byte, 0, len(data)+len(value))
	out = append(out, data[:start]...)
	out = append(out, value...)
	return append(out, data[end:]...)
}

func setValue(data []byte, path []string, literal []byte) ([]byte, error) {
	idx, err := indexTOML(data)
	if err != nil {
		return nil, err
	}
	if span, ok := idx.values[pathKey(path)]; ok {
		return replaceBytes(data, span.start, span.end, literal), nil
	}
	key, _ := json.Marshal(path[len(path)-1])
	line := string(key) + " = " + string(literal) + "\n"
	if len(path) == 2 {
		if parent, ok := idx.values[pathKey(path[:1])]; ok && data[parent.start] == '{' {
			lastValue := parent.start + 1
			for key, span := range idx.values {
				if strings.HasPrefix(key, pathKey(path[:1])+"\x00") && span.end > lastValue && span.end < parent.end {
					lastValue = span.end
				}
			}
			trailingComma := false
			for i := lastValue; i < parent.end-1; i++ {
				if data[i] == '#' {
					for i < parent.end-1 && data[i] != '\n' {
						i++
					}
				} else if data[i] == ',' {
					trailingComma = true
				}
			}
			separator := " "
			if lastValue > parent.start+1 && !trailingComma {
				separator = ", "
			}
			return replaceBytes(data, parent.end-1, parent.end-1, []byte(separator+strings.TrimSuffix(line, "\n")+" ")), nil
		}
		if end, ok := idx.tables[pathKey(path[:1])]; ok {
			prefix := ""
			if end > 0 && data[end-1] != '\n' {
				prefix = "\n"
			}
			return replaceBytes(data, end, end, []byte(prefix+line)), nil
		}
		parent, _ := json.Marshal(path[0])
		line = string(parent) + "." + line
	}
	position := idx.firstTable
	prefix := ""
	if position > 0 && data[position-1] != '\n' {
		prefix = "\n"
	}
	return replaceBytes(data, position, position, []byte(prefix+line)), nil
}
