package script

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Step is one action plus optional narration keyed by language code. In
// YAML the action is the step's key: `- click: "#save"` (decision 70).
type Step struct {
	Action    Action
	Narration map[string]string
}

// Action is what a step does. Name is the script key (goto, click, ...);
// Target is the URL or selector it acts on, "" when it has none. Failures and
// CLI progress show both.
type Action interface {
	Name() string
	Target() string
}

// The actions, one type per shape, so the executor needs no field checks.
type (
	Goto       struct{ URL string } // relative to baseUrl, or absolute (BR-010)
	Click      struct{ Selector string }
	Hover      struct{ Selector string }
	Fill       struct{ Selector, Value string } // an empty Value clears the field
	Select     struct{ Selector, Value string }
	Press      struct{ Key, Selector string } // Selector "" presses on the page
	ScrollTo   struct{ Y int }                // window scroll to y px
	ScrollInto struct{ Selector string }
	WaitFor    struct{ Selector string } // until visible
	Pause      struct{ D time.Duration }
)

// Screenshot captures a PNG (screenshots scripts only). At most one of
// Selector, FullPage and Clip is set (the schema checks); none means the
// viewport. Annotate, when set, is drawn first.
type Screenshot struct {
	Selector string
	FullPage bool
	Clip     *Clip
	Annotate *Annotate
}

// Clip is a region in px from the viewport's top-left corner.
type Clip struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Annotate draws markers around one element; at least one marker is set
// (the schema checks).
type Annotate struct {
	Selector string `json:"selector"`
	Box      bool   `json:"box,omitempty"`
	Arrow    bool   `json:"arrow,omitempty"`
	Dim      bool   `json:"dim,omitempty"`
	Label    string `json:"label,omitempty"`
}

func (Goto) Name() string       { return "goto" }
func (Click) Name() string      { return "click" }
func (Hover) Name() string      { return "hover" }
func (Fill) Name() string       { return "fill" }
func (Select) Name() string     { return "select" }
func (Press) Name() string      { return "press" }
func (ScrollTo) Name() string   { return "scroll" }
func (ScrollInto) Name() string { return "scroll" }
func (WaitFor) Name() string    { return "wait" }
func (Pause) Name() string      { return "wait" }
func (Screenshot) Name() string { return "screenshot" }

func (a Goto) Target() string       { return a.URL }
func (a Click) Target() string      { return a.Selector }
func (a Hover) Target() string      { return a.Selector }
func (a Fill) Target() string       { return a.Selector }
func (a Select) Target() string     { return a.Selector }
func (a Press) Target() string      { return a.Selector }
func (ScrollTo) Target() string     { return "" }
func (a ScrollInto) Target() string { return a.Selector }
func (a WaitFor) Target() string    { return a.Selector }
func (Pause) Target() string        { return "" }

// Target is the element shot's selector, else the annotated element's, else "".
func (a Screenshot) Target() string {
	if a.Selector == "" && a.Annotate != nil {
		return a.Annotate.Selector
	}
	return a.Selector
}

// selectorValue is the object form of fill and select.
type selectorValue struct {
	Selector string `json:"selector"`
	Value    string `json:"value"`
}

// screenshotValue is the object form of screenshot; `true` is the bare
// viewport shot.
type screenshotValue struct {
	Selector string    `json:"selector,omitempty"`
	FullPage bool      `json:"fullPage,omitempty"`
	Clip     *Clip     `json:"clip,omitempty"`
	Annotate *Annotate `json:"annotate,omitempty"`
}

// UnmarshalJSON reads the keyed form. It runs after schema validation, which
// guarantees exactly one action key with a value of the right shape, so the
// errors here only guard against a caller that skipped the schema.
func (s *Step) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*s = Step{}
	for k, raw := range fields {
		if k == "narration" {
			if err := json.Unmarshal(raw, &s.Narration); err != nil {
				return fmt.Errorf("narration: %w", err)
			}
			continue
		}
		if s.Action != nil {
			return fmt.Errorf("step has two actions: %s and %s", s.Action.Name(), k)
		}
		a, err := decodeAction(k, raw)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		s.Action = a
	}
	if s.Action == nil {
		return fmt.Errorf("step has no action")
	}
	return nil
}

// decodeAction picks the action type from the key and, for press, scroll,
// wait and screenshot, from whether the value is a string, a number or an object.
func decodeAction(key string, raw json.RawMessage) (Action, error) {
	var str string
	isString := json.Unmarshal(raw, &str) == nil
	switch key {
	case "goto":
		return Goto{URL: str}, stringErr(isString)
	case "click":
		return Click{Selector: str}, stringErr(isString)
	case "hover":
		return Hover{Selector: str}, stringErr(isString)
	case "fill":
		var v selectorValue
		err := json.Unmarshal(raw, &v)
		return Fill(v), err
	case "select":
		var v selectorValue
		err := json.Unmarshal(raw, &v)
		return Select(v), err
	case "press":
		if isString {
			return Press{Key: str}, nil
		}
		var v struct{ Key, Selector string }
		err := json.Unmarshal(raw, &v)
		return Press(v), err
	case "scroll":
		if isString {
			return ScrollInto{Selector: str}, nil
		}
		var v struct{ Y int }
		err := json.Unmarshal(raw, &v)
		return ScrollTo(v), err
	case "wait":
		if isString {
			return WaitFor{Selector: str}, nil
		}
		var ms int
		err := json.Unmarshal(raw, &ms)
		return Pause{D: time.Duration(ms) * time.Millisecond}, err
	case "screenshot":
		var v screenshotValue
		if string(bytes.TrimSpace(raw)) == "true" {
			return Screenshot{}, nil
		}
		err := json.Unmarshal(raw, &v)
		return Screenshot(v), err
	}
	return nil, fmt.Errorf("unknown action")
}

func stringErr(ok bool) error {
	if ok {
		return nil
	}
	return fmt.Errorf("want a string")
}

// MarshalJSON writes the keyed form, the inverse of UnmarshalJSON.
func (s Step) MarshalJSON() ([]byte, error) {
	var v any
	switch a := s.Action.(type) {
	case Goto:
		v = a.URL
	case Click:
		v = a.Selector
	case Hover:
		v = a.Selector
	case Fill:
		v = selectorValue(a)
	case Select:
		v = selectorValue(a)
	case Press:
		v = a.Key
		if a.Selector != "" {
			v = struct {
				Key      string `json:"key"`
				Selector string `json:"selector"`
			}{a.Key, a.Selector}
		}
	case ScrollTo:
		v = struct {
			Y int `json:"y"`
		}{a.Y}
	case ScrollInto:
		v = a.Selector
	case WaitFor:
		v = a.Selector
	case Pause:
		v = a.D.Milliseconds()
	case Screenshot:
		v = true
		if a != (Screenshot{}) {
			v = screenshotValue(a)
		}
	default:
		return nil, fmt.Errorf("unknown action %T", s.Action)
	}
	m := map[string]any{s.Action.Name(): v}
	if s.Narration != nil {
		m["narration"] = s.Narration
	}
	return json.Marshal(m)
}
