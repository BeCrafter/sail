package cmd

import (
	"testing"
)

// TestBuildTree 校验目录树构建:隐式中间目录、叶子写 size/isDir、空段过滤。
func TestBuildTree(t *testing.T) {
	entries := []tentry{
		{path: "a/b/c.txt", size: 3, isDir: false},
		{path: "a/d.log", size: 1, isDir: false},
		{path: "top.log", size: 5, isDir: false},
		{path: "a/", size: 0, isDir: true}, // 显式目录占位
	}
	root := buildTree(entries)

	a, ok := root.children["a"]
	if !ok {
		t.Fatalf("缺隐式目录 a")
	}
	if !a.isDir {
		t.Errorf("a 应为目录")
	}
	if b, ok := a.children["b"]; !ok {
		t.Errorf("缺隐式目录 a/b")
	} else if !b.isDir {
		t.Errorf("a/b 应为目录")
	} else if c := b.children["c.txt"]; c == nil || c.size != 3 || c.isDir {
		t.Errorf("a/b/c.txt 应为叶子 size=3,got %+v", c)
	}
	if d := a.children["d.log"]; d == nil || d.size != 1 {
		t.Errorf("a/d.log 叶子错误")
	}
	if top := root.children["top.log"]; top == nil || top.size != 5 {
		t.Errorf("top.log 叶子错误")
	}
}

// TestVisibleKids 校验排序与 -d 过滤。
func TestVisibleKids(t *testing.T) {
	root := buildTree([]tentry{
		{path: "z.txt", size: 1},
		{path: "a.txt", size: 1},
		{path: "dir/", size: 0, isDir: true},
	})

	// 不过滤:按名排序
	treeDirsOnly = false
	defer func() { treeDirsOnly = false }()
	kids := visibleKids(root)
	names := make([]string, len(kids))
	for i, k := range kids {
		names[i] = k.name
	}
	want := []string{"a.txt", "dir", "z.txt"}
	if !equalStrings(names, want) {
		t.Errorf("visibleKids 排序 = %v,期望 %v", names, want)
	}

	// -d:只留目录
	treeDirsOnly = true
	kids = visibleKids(root)
	if len(kids) != 1 || kids[0].name != "dir" {
		t.Errorf("-d 过滤后应只剩 dir,got %+v", kids)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
