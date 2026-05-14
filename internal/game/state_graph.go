package game

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

type StateGraph struct {
	mu      sync.RWMutex
	nodes   map[GameState]*StateNode
	edges   map[GameState][]StateTransition
}

type StateNode struct {
	State      GameState
	TemplateID string
	FirstSeen  time.Time
	Visits     int
}

type TransitionStep struct {
	From, To GameState
	Cost     time.Duration
}

type Path struct {
	Steps []TransitionStep
	Cost  time.Duration
}

func NewStateGraph() *StateGraph {
	return &StateGraph{
		nodes: make(map[GameState]*StateNode),
		edges: make(map[GameState][]StateTransition),
	}
}

func (g *StateGraph) AddNode(state GameState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.nodes[state]; !exists {
		g.nodes[state] = &StateNode{
			State:     state,
			FirstSeen: time.Now(),
		}
	}
	g.nodes[state].Visits++
}

func (g *StateGraph) AddTransition(from, to GameState, action TransitionAction, x, y int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.AddNodeUnlocked(from)
	g.AddNodeUnlocked(to)

	edge := StateTransition{
		From:     from,
		To:       to,
		Action:   action,
		X:        x,
		Y:        y,
		Duration: 1200 * time.Millisecond,
		Cost:     1200 * time.Millisecond,
	}

	edges := g.edges[from]
	for _, e := range edges {
		if e.To == to && e.Action == action && e.X == x && e.Y == y {
			return
		}
	}
	g.edges[from] = append(edges, edge)
}

func (g *StateGraph) AddNodeUnlocked(state GameState) {
	if _, exists := g.nodes[state]; !exists {
		g.nodes[state] = &StateNode{
			State:     state,
			FirstSeen: time.Now(),
		}
	}
}

func (g *StateGraph) HasState(state GameState) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, exists := g.nodes[state]
	return exists
}

func (g *StateGraph) TransitionsFrom(from GameState) []StateTransition {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edges[from]
}

func (g *StateGraph) ShortestPath(from, to GameState) *Path {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if from == to {
		return &Path{Cost: 0}
	}

	dist := make(map[GameState]time.Duration)
	prev := make(map[GameState]StateTransition)

	for s := range g.nodes {
		dist[s] = math.MaxInt64
	}
	dist[from] = 0

	pq := &priorityQueue{}
	heap.Init(pq)
	heap.Push(pq, &pathNode{state: from, dist: 0})

	for pq.Len() > 0 {
		current := heap.Pop(pq).(*pathNode)
		if current.dist > dist[current.state] {
			continue
		}

		if current.state == to {
			break
		}

		for _, edge := range g.edges[current.state] {
			next := edge.To
			cost := edge.Cost
			if current.dist+cost < dist[next] {
				dist[next] = current.dist + cost
				prev[next] = edge
				heap.Push(pq, &pathNode{state: next, dist: dist[next]})
			}
		}
	}

	if dist[to] == math.MaxInt64 {
		return nil
	}

	var steps []TransitionStep
	curr := to
	for {
		edge, ok := prev[curr]
		if !ok {
			break
		}
		steps = append(steps, TransitionStep{From: edge.From, To: edge.To, Cost: edge.Cost})
		curr = edge.From
	}

	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}

	var totalCost time.Duration
	for _, s := range steps {
		totalCost += s.Cost
	}

	return &Path{Steps: steps, Cost: totalCost}
}

func (g *StateGraph) AddStateChange(from, to GameState) {
	g.AddTransition(from, to, ActionNone, 0, 0)
}

func (g *StateGraph) AllStates() []GameState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var states []GameState
	for s := range g.nodes {
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool {
		return int(states[i]) < int(states[j])
	})
	return states
}

func (g *StateGraph) StateCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

func (g *StateGraph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := 0
	for _, edges := range g.edges {
		n += len(edges)
	}
	return n
}

func (g *StateGraph) Nodes() map[GameState]*StateNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[GameState]*StateNode, len(g.nodes))
	for k, v := range g.nodes {
		result[k] = v
	}
	return result
}

func (g *StateGraph) Save(path string) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	type savedNode struct {
		State     int   `json:"state"`
		Visits    int   `json:"visits"`
		FirstSeen int64 `json:"first_seen"`
	}

	type savedEdge struct {
		From, To int           `json:"from_to"`
		Action   int           `json:"action"`
		X, Y     int           `json:"coords"`
		CostMs   int64         `json:"cost_ms"`
	}

	data := struct {
		Nodes map[int]savedNode `json:"nodes"`
		Edges []savedEdge       `json:"edges"`
	}{
		Nodes: make(map[int]savedNode),
		Edges: []savedEdge{},
	}

	for s, n := range g.nodes {
		data.Nodes[int(s)] = savedNode{
			State:     int(s),
			Visits:    n.Visits,
			FirstSeen: n.FirstSeen.Unix(),
		}
	}

	for from, edges := range g.edges {
		for _, e := range edges {
			data.Edges = append(data.Edges, savedEdge{
				From:   int(from),
				To:     int(e.To),
				Action: int(e.Action),
				X:      e.X,
				Y:      e.Y,
				CostMs: e.Cost.Milliseconds(),
			})
		}
	}

	blob, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal graph: %w", err)
	}

	return writeFile(path, blob)
}

func (g *StateGraph) Load(path string) error {
	blob, err := readFile(path)
	if err != nil {
		return fmt.Errorf("read graph: %w", err)
	}

	type savedNode struct {
		State     int   `json:"state"`
		Visits    int   `json:"visits"`
		FirstSeen int64 `json:"first_seen"`
	}

	type savedEdge struct {
		From, To int `json:"from_to"`
		Action   int `json:"action"`
		X, Y     int `json:"coords"`
		CostMs   int64 `json:"cost_ms"`
	}

	data := struct {
		Nodes map[int]savedNode `json:"nodes"`
		Edges []savedEdge       `json:"edges"`
	}{}

	if err := json.Unmarshal(blob, &data); err != nil {
		return fmt.Errorf("unmarshal graph: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	for i, n := range data.Nodes {
		g.nodes[GameState(i)] = &StateNode{
			State:     GameState(n.State),
			Visits:    n.Visits,
			FirstSeen: time.Unix(n.FirstSeen, 0),
		}
	}

	for _, e := range data.Edges {
		g.edges[GameState(e.From)] = append(g.edges[GameState(e.From)], StateTransition{
			From:   GameState(e.From),
			To:     GameState(e.To),
			Action: TransitionAction(e.Action),
			X:      e.X,
			Y:      e.Y,
			Cost:   time.Duration(e.CostMs) * time.Millisecond,
		})
	}

	return nil
}

type pathNode struct {
	state GameState
	dist  time.Duration
}

type priorityQueue []*pathNode

func (pq priorityQueue) Len() int { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool {
	return pq[i].dist < pq[j].dist
}
func (pq priorityQueue) Swap(i, j int) { pq[i], pq[j] = pq[j], pq[i] }
func (pq *priorityQueue) Push(x any) { *pq = append(*pq, x.(*pathNode)) }
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

var readFile = os.ReadFile
var writeFile = func(path string, data []byte) error { return os.WriteFile(path, data, 0644) }

type fileReader func(string) ([]byte, error)
type fileWriter func(string, []byte) error

func SetFileIO(r fileReader, w fileWriter) {
	readFile = r
	writeFile = w
}

type StateGraphManager struct {
	graph  *StateGraph
	path   string
	mu     sync.Mutex
	dirty  bool
}

func NewGraphManager(path string) *StateGraphManager {
	return &StateGraphManager{path: path, graph: NewStateGraph()}
}

func (m *StateGraphManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graph.Load(m.path)
}

func (m *StateGraphManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graph.Save(m.path)
}

func (m *StateGraphManager) Add(state GameState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graph.AddNode(state)
	m.dirty = true
}

func (m *StateGraphManager) AddTransition(from, to GameState, action TransitionAction, x, y int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graph.AddTransition(from, to, action, x, y)
	m.dirty = true
}

func (m *StateGraphManager) Navigate(from, to GameState) *Path {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graph.ShortestPath(from, to)
}

func (m *StateGraphManager) ShouldSave() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirty
}

func (m *StateGraphManager) MarkSaved() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirty = false
}

func (m *StateGraphManager) Stats() (states, edges int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.graph.StateCount(), m.graph.EdgeCount()
}