package computer

import "testing"

// buildTestTree constructs a 5-node tree where 2 are non-interactable layout
// containers (no text, not clickable/scrollable/editable) and 3 are
// interactable. Used to verify assign assigns refs only to interactables in
// pre-order and that numbering is dense 1..N.
//
//	root (container, NOT interactable)
//	├── btn1 (Button, clickable)        → ref 1
//	├── layout (container, NOT interactable)
//	│   └── edit (EditText, editable)   → ref 2
//	└── txt (TextView, text="hello")    → ref 3
func buildTestTree() *UINode {
	return &UINode{
		ClassName: "android.widget.FrameLayout",
		Children: []UINode{
			{ClassName: "android.widget.Button", Clickable: true, Bounds: Bounds{Left: 0, Top: 0, Right: 100, Bottom: 50}},
			{ClassName: "android.widget.LinearLayout", Children: []UINode{
				{ClassName: "android.widget.EditText", Editable: true, Text: "query", Bounds: Bounds{Left: 10, Top: 60, Right: 200, Bottom: 110}},
			}},
			{ClassName: "android.widget.TextView", Text: "hello", Bounds: Bounds{Left: 0, Top: 120, Right: 300, Bottom: 160}},
		},
	}
}

func TestRef_Assign_InteractableOnly(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	els := r.assign(buildTestTree())
	if len(els) != 3 {
		t.Fatalf("assign returned %d elements, want 3", len(els))
	}
	// Dense 1..3 in pre-order: Button, EditText, TextView.
	wantClasses := []string{"android.widget.Button", "android.widget.EditText", "android.widget.TextView"}
	for i, el := range els {
		if el.Ref != i+1 {
			t.Errorf("element %d Ref = %d, want %d", i, el.Ref, i+1)
		}
		if el.ClassName != wantClasses[i] {
			t.Errorf("element %d ClassName = %q, want %q", i, el.ClassName, wantClasses[i])
		}
	}
}

func TestRef_Resolve_ReturnsStoredBounds(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	r.assign(buildTestTree())
	// Ref 2 is the EditText with bounds [10,60,200,110].
	el, ok := r.resolve(2)
	if !ok {
		t.Fatal("resolve(2) = false, want true")
	}
	if el.Bounds.Left != 10 || el.Bounds.Top != 60 || el.Bounds.Right != 200 || el.Bounds.Bottom != 110 {
		t.Errorf("ref 2 bounds = %+v, want {10,60,200,110}", el.Bounds)
	}
	if el.Text != "query" {
		t.Errorf("ref 2 Text = %q, want query", el.Text)
	}
}

func TestRef_Resolve_Absent(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	r.assign(buildTestTree())
	if _, ok := r.resolve(99); ok {
		t.Error("resolve(99) = true, want false (out of range)")
	}
}

func TestRef_Reassign_ResetsNumbering(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	r.assign(buildTestTree())
	// Re-assign: numbering must restart at 1 and old ref 3 must be gone if
	// the new tree is smaller.
	small := &UINode{ClassName: "android.widget.Button", Clickable: true}
	els := r.assign(small)
	if len(els) != 1 {
		t.Fatalf("re-assign returned %d elements, want 1", len(els))
	}
	if els[0].Ref != 1 {
		t.Errorf("re-assign ref = %d, want 1", els[0].Ref)
	}
	if _, ok := r.resolve(3); ok {
		t.Error("resolve(3) after re-assign = true, want false (stale ref must be gone)")
	}
}

func TestRef_Clear(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	r.assign(buildTestTree())
	r.clear()
	if _, ok := r.resolve(1); ok {
		t.Error("resolve(1) after clear = true, want false")
	}
	// next must restart at 1 after clear + assign.
	els := r.assign(buildTestTree())
	if els[0].Ref != 1 {
		t.Errorf("after clear+assign ref = %d, want 1", els[0].Ref)
	}
}

func TestRef_Assign_NilRoot(t *testing.T) {
	t.Parallel()
	r := newRefRegistry()
	els := r.assign(nil)
	if len(els) != 0 {
		t.Errorf("assign(nil) returned %d elements, want 0", len(els))
	}
}

func TestIsInteractable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		node UINode
		want bool
	}{
		{"clickable", UINode{Clickable: true}, true},
		{"scrollable", UINode{Scrollable: true}, true},
		{"editable", UINode{Editable: true}, true},
		{"with text", UINode{Text: "hi"}, true},
		{"with content desc", UINode{ContentDescription: "desc"}, true},
		{"plain container", UINode{ClassName: "android.widget.FrameLayout"}, false},
		{"empty", UINode{}, false},
	}
	for _, c := range cases {
		if got := isInteractable(&c.node); got != c.want {
			t.Errorf("isInteractable(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNodeToElement_CopiesFields(t *testing.T) {
	t.Parallel()
	n := &UINode{
		ClassName: "android.widget.Button",
		Text:      "OK",
		Clickable: true,
		Bounds:    Bounds{Left: 1, Top: 2, Right: 3, Bottom: 4},
		Children:  []UINode{{ClassName: "child"}},
	}
	el := nodeToElement(n)
	if el.ClassName != "android.widget.Button" {
		t.Errorf("ClassName = %q", el.ClassName)
	}
	if el.Text != "OK" {
		t.Errorf("Text = %q", el.Text)
	}
	if !el.Clickable {
		t.Error("Clickable = false")
	}
	if el.Bounds.Left != 1 {
		t.Errorf("Bounds.Left = %d", el.Bounds.Left)
	}
	// Ref is NOT copied by nodeToElement (assign sets it).
	if el.Ref != 0 {
		t.Errorf("Ref = %d, want 0 (set by assign)", el.Ref)
	}
}
