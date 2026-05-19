package workflow

import (
	"errors"
	"fmt"
	"sort"
)

// Input defines a workflow input parameter
type Input struct {
	Name        string      `json:"name" yaml:"name"`
	Type        string      `json:"type" yaml:"type"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Default     interface{} `json:"default,omitempty" yaml:"default,omitempty"`
}

func (i *Input) IsRequired() bool {
	return i.Default == nil
}

// Output defines a workflow output parameter
type Output struct {
	Name     string `json:"name" yaml:"name"`
	Variable string `json:"variable" yaml:"variable"`
	// Branch names the execution branch to extract the output value from.
	// Defaults to "main" when empty.
	Branch      string `json:"branch,omitempty" yaml:"branch,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Options are used to configure a workflow.
type Options struct {
	Name        string         `json:"name" yaml:"name"`
	Steps       []*Step        `json:"steps" yaml:"steps"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Inputs      []*Input       `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs     []*Output      `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	State       map[string]any `json:"state,omitempty" yaml:"state,omitempty"`
	// StartAt names the step that the first execution branch begins on.
	// When empty, the first step in Steps is the start step. Validated
	// at New() time to reference an existing step.
	StartAt string `json:"start_at,omitempty" yaml:"start_at,omitempty"`
	// Groups is purely organizational metadata for editors (visual
	// sub-flows / parent-child). The executor ignores it entirely —
	// execution is flat and name-based. It is validated for internal
	// consistency and round-trips through save → validate → reload.
	Groups []*Group `json:"groups,omitempty" yaml:"groups,omitempty"`
}

// Group is a named, optionally collapsible visual region containing
// steps (referenced by Step.GroupID). Identity is the stable ID,
// decoupled from step names. Layout hints (x/y/width/height) let an
// editor reopen a saved sub-flow where the author left it.
type Group struct {
	ID          string  `json:"id" yaml:"id"`
	Name        string  `json:"name,omitempty" yaml:"name,omitempty"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	ParentID    string  `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Collapsed   bool    `json:"collapsed,omitempty" yaml:"collapsed,omitempty"`
	X           float64 `json:"x,omitempty" yaml:"x,omitempty"`
	Y           float64 `json:"y,omitempty" yaml:"y,omitempty"`
	Width       float64 `json:"width,omitempty" yaml:"width,omitempty"`
	Height      float64 `json:"height,omitempty" yaml:"height,omitempty"`
}

// Workflow defines a repeatable process as a graph of steps to be executed.
type Workflow struct {
	name         string
	description  string
	inputs       []*Input
	outputs      []*Output
	steps        []*Step
	stepsByName  map[string]*Step
	start        *Step
	initialState map[string]any
	groups       []*Group
}

// New returns a new Workflow configured with the given options.
//
// New runs structural validation and fails fast on any problem it
// finds, returning a *ValidationError with the full list of problems.
// Structural validation does not consult the activity registry or
// the script compiler — that binding-layer validation runs during
// NewExecution.
func New(opts Options) (*Workflow, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("workflow: name required")
	}
	if len(opts.Steps) == 0 {
		return nil, fmt.Errorf("workflow: steps required")
	}

	stepsByName := make(map[string]*Step, len(opts.Steps))
	var dupes []ValidationProblem
	for _, step := range opts.Steps {
		if step.Name == "" {
			dupes = append(dupes, ValidationProblem{
				Message: "empty step name",
				Err:     ErrEmptyStepName,
			})
			continue
		}
		if _, exists := stepsByName[step.Name]; exists {
			dupes = append(dupes, ValidationProblem{
				Step:    step.Name,
				Message: fmt.Sprintf("duplicate step name %q", step.Name),
				Err:     ErrDuplicateStepName,
			})
			continue
		}
		stepsByName[step.Name] = step
	}

	start := opts.Steps[0]
	if opts.StartAt != "" {
		if s, ok := stepsByName[opts.StartAt]; ok {
			start = s
		} else {
			dupes = append(dupes, ValidationProblem{
				Message: fmt.Sprintf("start step %q not found", opts.StartAt),
				Err:     ErrUnknownStartStep,
			})
		}
	}

	wf := &Workflow{
		name:         opts.Name,
		description:  opts.Description,
		inputs:       opts.Inputs,
		outputs:      opts.Outputs,
		steps:        opts.Steps,
		stepsByName:  stepsByName,
		start:        start,
		initialState: opts.State,
		groups:       opts.Groups,
	}

	if err := wf.Validate(); err != nil {
		// Merge duplicate-name problems found while building stepsByName.
		var ve *ValidationError
		if errors.As(err, &ve) && len(dupes) > 0 {
			ve.Problems = append(dupes, ve.Problems...)
			return nil, ve
		}
		return nil, err
	}
	if len(dupes) > 0 {
		return nil, &ValidationError{Problems: dupes}
	}
	return wf, nil
}

// Name returns the workflow name
func (w *Workflow) Name() string {
	return w.name
}

// Description returns the workflow description
func (w *Workflow) Description() string {
	return w.description
}

// Inputs returns the workflow inputs
func (w *Workflow) Inputs() []*Input {
	return w.inputs
}

// Outputs returns the workflow outputs
func (w *Workflow) Outputs() []*Output {
	return w.outputs
}

// Groups returns the workflow's visual group metadata (editor-only;
// the executor does not use it).
func (w *Workflow) Groups() []*Group {
	return w.groups
}

// Steps returns the workflow steps
func (w *Workflow) Steps() []*Step {
	return w.steps
}

// Start returns the workflow start step
func (w *Workflow) Start() *Step {
	return w.start
}

// InitialState returns the workflow initial state
func (w *Workflow) InitialState() map[string]any {
	return w.initialState
}

// GetStep returns a step by name
func (w *Workflow) GetStep(name string) (*Step, bool) {
	step, ok := w.stepsByName[name]
	return step, ok
}

// StepNames returns the names of all steps in the workflow
func (w *Workflow) StepNames() []string {
	names := make([]string, 0, len(w.stepsByName))
	for name := range w.stepsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
