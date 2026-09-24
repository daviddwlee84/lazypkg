package config

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

var setName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func normalizeIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = catalog.Normalize(id)
		if _, ok := catalog.Lookup(id); !ok {
			return nil, fmt.Errorf("unknown manager %q", id)
		}
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out, nil
}

func (c Config) validate() error {
	if _, err := normalizeIDs(c.Managers); err != nil {
		return fmt.Errorf("managers: %w", err)
	}
	if _, err := normalizeIDs(c.ManagerOrder); err != nil {
		return fmt.Errorf("manager_order: %w", err)
	}
	for name, ids := range c.ManagerSets {
		if !setName.MatchString(name) {
			return fmt.Errorf("invalid manager set name %q; use letters, digits, - or _", name)
		}
		if len(ids) == 0 {
			return fmt.Errorf("manager set %q is empty", name)
		}
		if _, err := normalizeIDs(ids); err != nil {
			return fmt.Errorf("manager set %q: %w", name, err)
		}
	}
	if c.DefaultManagerSet != "" {
		if _, ok := c.ManagerSets[c.DefaultManagerSet]; !ok {
			return fmt.Errorf("default_manager_set references unknown set %q", c.DefaultManagerSet)
		}
	}
	return nil
}

func (c Config) Select(q domain.PackageQuery) ([]string, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	selectors := 0
	if q.Managers != nil {
		selectors++
	}
	if q.Group != "" {
		selectors++
	}
	if q.Set != "" {
		selectors++
	}
	if selectors > 1 {
		return nil, fmt.Errorf("manager, group and set selectors are mutually exclusive")
	}
	if q.Managers != nil {
		if len(q.Managers) == 0 {
			return nil, fmt.Errorf("select at least one manager; an empty explicit selection is not the default set")
		}
		return normalizeIDs(q.Managers)
	}
	if q.Group != "" {
		ids, ok := catalog.Groups()[q.Group]
		if !ok {
			return nil, fmt.Errorf("unknown manager group %q", q.Group)
		}
		return ids, nil
	}
	name := q.Set
	if selectors == 0 {
		name = c.DefaultManagerSet
	}
	if name != "" {
		ids, ok := c.ManagerSets[name]
		if !ok {
			return nil, fmt.Errorf("unknown manager set %q", name)
		}
		return normalizeIDs(ids)
	}
	if len(c.Managers) > 0 {
		return normalizeIDs(c.Managers)
	}
	return catalog.DefaultIDs(), nil
}

func (c Config) Order(q domain.PackageQuery) ([]string, error) {
	ids, err := c.Select(q)
	if err != nil {
		return nil, err
	}
	if q.Managers != nil || q.Set != "" || (q.Group == "" && c.DefaultManagerSet != "") {
		return ids, nil
	}
	return c.priorityOrder(ids)
}

func (c Config) priorityOrder(ids []string) ([]string, error) {
	priority, err := normalizeIDs(c.ManagerOrder)
	if err != nil {
		return nil, err
	}
	ranks := map[string]int{}
	for i, id := range priority {
		ranks[id] = i
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, oka := ranks[ids[i]]
		b, okb := ranks[ids[j]]
		if oka != okb {
			return oka
		}
		if oka && a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}
