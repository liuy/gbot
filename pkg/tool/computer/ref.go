package computer

import "sync"

// refRegistry holds the most recent Screen's ref→ElementRef mapping. It is
// the contract between Screen() (which assigns refs) and ClickElement/
// OpenMenuElement (which resolve refs). A ref is invalidated by the next
// Screen() call or by Disconnect/Connect (device switch) — calling
// ClickElement with a stale ref is an error.
//
// The assigned flag distinguishes "no Screen yet" (resolveRef returns
// no-screen) from "Screen returned zero elements" (resolveRef returns
// ref-not-found). Without it a successful Screen with no interactables would
// be indistinguishable from never having called Screen.
type refRegistry struct {
	mu       sync.Mutex
	next     int
	assigned bool
	byRef    map[int]ElementRef
}

func newRefRegistry() *refRegistry {
	return &refRegistry{byRef: make(map[int]ElementRef)}
}

// assign walks the UINode tree in pre-order and assigns a ref to every
// interactable node. Returns the flattened list and populates byRef. Each
// call resets the numbering so refs are dense 1..N for the current screen.
func (r *refRegistry) assign(root *UINode) []ElementRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRef = make(map[int]ElementRef)
	r.next = 1
	r.assigned = true
	var out []ElementRef
	r.walk(root, &out)
	return out
}

// walk is the recursive pre-order helper. assign holds the lock for the whole
// traversal so the numbering is atomic w.r.t. concurrent resolve calls.
func (r *refRegistry) walk(n *UINode, out *[]ElementRef) {
	if n == nil {
		return
	}
	if isInteractable(n) {
		ref := r.next
		r.next++
		el := nodeToElement(n)
		el.Ref = ref
		r.byRef[ref] = el
		*out = append(*out, el)
	}
	for i := range n.Children {
		r.walk(&n.Children[i], out)
	}
}

// resolve returns the ElementRef for ref, or ok=false if absent/stale.
func (r *refRegistry) resolve(ref int) (ElementRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.byRef[ref]
	return el, ok
}

// hasScreen reports whether assign has been called at least once since the
// last clear. Used by resolveRef to distinguish "no Screen yet" from "Screen
// returned zero elements".
func (r *refRegistry) hasScreen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.assigned
}

// clear drops all refs. Called on Disconnect and on device switch in Connect
// so a ref captured against device A can never be resolved against device B.
func (r *refRegistry) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRef = make(map[int]ElementRef)
	r.next = 1
	r.assigned = false
}

// isInteractable marks a node for ref assignment. A node is addressable when
// the model could plausibly want to click/long-press/type into it: anything
// clickable/scrollable/editable, plus nodes carrying a textual label (text
// or content description). Pure layout containers get no ref.
func isInteractable(n *UINode) bool {
	return n.Clickable || n.Scrollable || n.Editable || n.Text != "" || n.ContentDescription != ""
}

// nodeToElement copies the UINode fields into an ElementRef (minus children,
// which are omitted from the ref-list rendering).
func nodeToElement(n *UINode) ElementRef {
	return ElementRef{
		ClassName:          n.ClassName,
		Text:               n.Text,
		ContentDescription: n.ContentDescription,
		ViewID:             n.ViewID,
		PackageName:        n.PackageName,
		Clickable:          n.Clickable,
		Scrollable:         n.Scrollable,
		Editable:           n.Editable,
		Enabled:            n.Enabled,
		Checked:            n.Checked,
		Focused:            n.Focused,
		Selected:           n.Selected,
		Bounds:             n.Bounds,
	}
}
