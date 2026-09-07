package plan

import (
	"fmt"
	"slices"
	"strings"
)

type Diagnostic struct {
	Code    string `json:"code"`
	Node    string `json:"node,omitempty"`
	Related string `json:"related,omitempty"`
	Message string `json:"message"`
}

func Check(d Definition, s State) []Diagnostic {
	diagnostics := []Diagnostic{}
	seen := map[string]bool{}
	for _, n := range d.Nodes {
		if seen[n.ID] {
			diagnostics = append(diagnostics, Diagnostic{Code: "DuplicateNodeId", Node: n.ID, Message: "Use one unique id per node."})
		}
		seen[n.ID] = true
		if n.Kind == "Selector" && (len(n.Options) < 2 || len(unique(n.Options)) != len(n.Options)) {
			diagnostics = append(diagnostics, Diagnostic{Code: "DegenerateSelector", Node: n.ID, Message: "Provide at least two distinct options."})
		}
		for _, need := range n.Dependencies() {
			producer, ok := d.Find(need.Node)
			if !ok {
				diagnostics = append(diagnostics, Diagnostic{Code: "UnregisteredNeed", Node: n.ID, Related: need.Node, Message: need.String() + " has no producer in nodes."})
				continue
			}
			valid := need.Kind == "Artifact" && producer.Kind == "Action" || need.Kind == "Evidence" && producer.Kind != "Selector" || need.Kind == "Fork" && producer.Kind == "Selector" || need.Kind == "Grant" && producer.Kind == "Action"
			if !valid {
				diagnostics = append(diagnostics, Diagnostic{Code: "WrongProducerKind", Node: n.ID, Related: need.Node, Message: fmt.Sprintf("%s cannot reference %s", need.String(), producer.Kind)})
			}
		}
		if n.Probe != nil {
			producer, ok := d.Find(n.Probe.ID)
			if !ok || producer.Kind != "Guard" {
				diagnostics = append(diagnostics, Diagnostic{Code: "InvalidProbe", Node: n.ID, Related: n.Probe.ID, Message: "probe must reference a registered Guard."})
			}
		}
	}
	_, cyclic := d.order()
	if len(cyclic) > 0 {
		diagnostics = append(diagnostics, Diagnostic{Code: "Cycle", Node: cyclic[0], Message: "Dependency cycle: " + strings.Join(cyclic, " -> ")})
	}
	for i, left := range d.Nodes {
		if left.Kind != "Action" {
			continue
		}
		for _, right := range d.Nodes[i+1:] {
			if right.Kind != "Action" || d.reaches(left.ID, right.ID) || d.reaches(right.ID, left.ID) {
				continue
			}
			var overlap string
			for _, a := range s.EffectiveGrant(left) {
				for _, b := range s.EffectiveGrant(right) {
					if overlaps(a, b) {
						overlap = a
						break
					}
				}
				if overlap != "" {
					break
				}
			}
			if overlap != "" {
				diagnostics = append(diagnostics, Diagnostic{Code: "FootprintCollision", Node: left.ID, Related: right.ID, Message: "Unordered Actions share " + overlap + "; narrow grants or declare the real dependency."})
			}
		}
	}
	components := d.components()
	if len(components) > 1 {
		slices.SortStableFunc(components, func(a, b []string) int {
			if len(a) != len(b) {
				return len(b) - len(a)
			}
			return strings.Compare(a[0], b[0])
		})
		for _, c := range components[1:] {
			diagnostics = append(diagnostics, Diagnostic{Code: "DisconnectedComponent", Node: c[0], Message: "Disconnected nodes: " + strings.Join(c, ", ") + "; connect their outcome or use a separate plan."})
		}
	}
	return diagnostics
}

func unique(values []string) []string {
	out := []string{}
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func overlaps(a, b string) bool {
	normalize := func(path string) string {
		withoutStars := strings.TrimRight(path, "*")
		if strings.HasSuffix(withoutStars, "/") {
			path = withoutStars
		}
		return strings.TrimRight(path, "/")
	}
	a, b = normalize(a), normalize(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (d Definition) reaches(from, to string) bool {
	seen := map[string]bool{}
	queue := []string{from}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		n, ok := d.Find(id)
		if !ok {
			continue
		}
		for _, need := range n.Dependencies() {
			if need.Node == to {
				return true
			}
			queue = append(queue, need.Node)
		}
	}
	return false
}

func (d Definition) order() ([]string, []string) {
	order := []string{}
	colors := map[string]int{}
	var cycle []string
	var visit func(string, []string)
	visit = func(id string, path []string) {
		if colors[id] == 2 || cycle != nil {
			return
		}
		if colors[id] == 1 {
			cycle = append(slices.Clone(path), id)
			return
		}
		n, ok := d.Find(id)
		if !ok {
			return
		}
		colors[id] = 1
		for _, need := range n.Dependencies() {
			visit(need.Node, append(slices.Clone(path), id))
		}
		colors[id] = 2
		order = append(order, id)
	}
	for _, n := range d.Nodes {
		visit(n.ID, nil)
	}
	return order, cycle
}

func (d Definition) components() [][]string {
	adj := map[string][]string{}
	for _, n := range d.Nodes {
		for _, need := range n.Dependencies() {
			if _, ok := d.Find(need.Node); ok {
				adj[n.ID] = append(adj[n.ID], need.Node)
				adj[need.Node] = append(adj[need.Node], n.ID)
			}
		}
	}
	seen := map[string]bool{}
	result := [][]string{}
	for _, n := range d.Nodes {
		if seen[n.ID] {
			continue
		}
		component := []string{}
		queue := []string{n.ID}
		for len(queue) > 0 {
			id := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			component = append(component, id)
			queue = append(queue, adj[id]...)
		}
		slices.Sort(component)
		result = append(result, component)
	}
	return result
}

func (d Definition) CriticalPath() []string {
	order, cyclic := d.order()
	if len(cyclic) > 0 {
		return []string{}
	}
	paths := map[string][]string{}
	best := []string{}
	for _, id := range order {
		n, _ := d.Find(id)
		prefix := []string{}
		for _, need := range n.Dependencies() {
			if len(paths[need.Node]) > len(prefix) {
				prefix = paths[need.Node]
			}
		}
		paths[id] = append(slices.Clone(prefix), id)
		if len(paths[id]) > len(best) {
			best = paths[id]
		}
	}
	return best
}
