package bash

import (
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestIsSearchOrReadBashCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    tool.SearchReadKind
	}{
		{"grep", `grep -r "pattern" .`, tool.SearchReadKind{IsSearch: true}},
		{"rg", `rg "foo" --glob '*.go'`, tool.SearchReadKind{IsSearch: true}},
		{"find", `find . -name "*.go"`, tool.SearchReadKind{IsSearch: true}},
		{"cat", "cat file.txt", tool.SearchReadKind{IsRead: true}},
		{"head", "head -20 file.go", tool.SearchReadKind{IsRead: true}},
		{"wc", "wc -l file.go", tool.SearchReadKind{IsRead: true}},
		{"ls", "ls -la", tool.SearchReadKind{IsList: true}},
		{"tree", "tree src/", tool.SearchReadKind{IsList: true}},
		{"du", "du -sh .", tool.SearchReadKind{IsList: true}},
		{"git commit", `git commit -m "fix"`, tool.SearchReadKind{}},
		{"npm test", "npm test", tool.SearchReadKind{}},
		{"python", "python script.py", tool.SearchReadKind{}},
		{"echo only", `echo "hello"`, tool.SearchReadKind{}},
		{"empty", "", tool.SearchReadKind{}},
		// Compound commands
		{"pipe cat grep", "cat file | grep pattern", tool.SearchReadKind{IsSearch: true, IsRead: true}},
		{"pipe wc sort", "wc -l file.go | sort", tool.SearchReadKind{IsRead: true}},
		{"and find echo", `find . -name "*.go" && echo "found"`, tool.SearchReadKind{IsSearch: true}},
		{"and echo git", `echo "hello" && git commit`, tool.SearchReadKind{}},
		{"or grep echo", `grep foo || echo "not found"`, tool.SearchReadKind{IsSearch: true}},
		{"semi ls cat", "ls dir1; cat file", tool.SearchReadKind{IsList: true, IsRead: true}},
		{"redirect cat", "cat file > output.txt", tool.SearchReadKind{IsRead: true}},
		{"pipe ls head", "ls -la | head -5", tool.SearchReadKind{IsList: true, IsRead: true}},
		// Quoted strings preserved
		{"grep quoted", `grep 'hello world' file.txt`, tool.SearchReadKind{IsSearch: true}},

		// xargs as neutral passthrough
		{"pipe find xargs grep", `find . -name "*.go" | xargs grep "pattern"`, tool.SearchReadKind{IsSearch: true}},
		{"pipe grep xargs cat", `grep -rl "pattern" | xargs cat`, tool.SearchReadKind{IsSearch: true, IsRead: true}},
		{"pipe find xargs -0 grep", `find . -print0 | xargs -0 grep "pattern"`, tool.SearchReadKind{IsSearch: true}},
		{"pipe find xargs -I grep", `find . -type f | xargs -I % grep "pattern" %`, tool.SearchReadKind{IsSearch: true}},

		// sed: search when no -i flag
		{"sed print", `sed -n '10,20p' file.txt`, tool.SearchReadKind{IsSearch: true}},
		{"sed substitute stdout", `sed 's/old/new/g' file.txt`, tool.SearchReadKind{IsSearch: true}},
		{"sed -i inplace", `sed -i 's/old/new/g' file.txt`, tool.SearchReadKind{}},
		{"sed -i.bak", `sed -i.bak 's/old/new/g' file.txt`, tool.SearchReadKind{}},

		// awk: search by default
		{"awk print", `awk '{print $1}' file.txt`, tool.SearchReadKind{IsSearch: true}},
		{"awk pattern", `awk '/pattern/' file.txt`, tool.SearchReadKind{IsSearch: true}},
		{"awk redirect out", `awk '{print > "out.txt"}' file.txt`, tool.SearchReadKind{}},
		{"awk system", `awk '{system("rm foo")}' file.txt`, tool.SearchReadKind{}},
		{"awk -i inplace", `awk -i inplace '{print}' file.txt`, tool.SearchReadKind{}},

		// cd is neutral — does not break search/read classification
		{"cd && grep", `cd /repo && grep -n "foo" file.go`, tool.SearchReadKind{IsSearch: true}},
		{"cd && rg", `cd /repo && rg "foo"`, tool.SearchReadKind{IsSearch: true}},
		{"cd && cat", `cd /tmp && cat log.txt`, tool.SearchReadKind{IsRead: true}},
		{"cd && ls", `cd /repo && ls -la`, tool.SearchReadKind{IsList: true}},
		{"cd && git commit", `cd /repo && git commit -m "x"`, tool.SearchReadKind{}},
		{"cd only", `cd /repo`, tool.SearchReadKind{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSearchOrReadBashCommand(tt.command)
			if got != tt.want {
				t.Errorf("isSearchOrReadBashCommand(%q) = %+v, want %+v", tt.command, got, tt.want)
			}
		})
	}
}

func TestSplitOnOperators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  string
		want int // expected number of parts
	}{
		{"simple", "grep foo", 1},
		{"pipe", "cat file | grep foo", 2},
		{"and", `echo hi && ls`, 2},
		{"or", `grep foo || echo no`, 2},
		{"semi", "ls; pwd", 2},
		{"complex", `cat file | grep foo && echo yes`, 3},
		{"quoted pipe", `echo "hello|world"`, 1},
		{"quoted and", `echo 'hello&&world'`, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parts := splitOnOperators(tt.cmd)
			if len(parts) != tt.want {
				t.Errorf("splitOnOperators(%q) returned %d parts, want %d: %v", tt.cmd, len(parts), tt.want, parts)
			}
		})
	}
}
