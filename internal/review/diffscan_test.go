package review

import "testing"

func TestParseUnifiedDiff_AddedLinesAndLineNumbers(t *testing.T) {
	diff := `diff --git a/app/Http/Controllers/UserController.php b/app/Http/Controllers/UserController.php
index 111..222 100644
--- a/app/Http/Controllers/UserController.php
+++ b/app/Http/Controllers/UserController.php
@@ -10,2 +10,4 @@ public function index()
 {
+    foreach ($users as $user) {
+        echo $user->profile->name;
+    }
 }
`
	files := ParseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "app/Http/Controllers/UserController.php" {
		t.Fatalf("path = %q", files[0].Path)
	}
	if len(files[0].Added) != 3 {
		t.Fatalf("expected 3 added lines, got %d", len(files[0].Added))
	}
	if files[0].Added[0].LineNo != 11 {
		t.Fatalf("first added line number = %d, want 11", files[0].Added[0].LineNo)
	}
}

func TestParseUnifiedDiff_MultipleFiles(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1,0 +1,1 @@
+package a
diff --git a/b.go b/b.go
index 111..222 100644
--- a/b.go
+++ b/b.go
@@ -1,0 +1,1 @@
+package b
`
	files := ParseUnifiedDiff(diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "a.go" || files[1].Path != "b.go" {
		t.Fatalf("paths = %q, %q", files[0].Path, files[1].Path)
	}
}

func TestParseUnifiedDiff_EmptyReturnsNil(t *testing.T) {
	if got := ParseUnifiedDiff(""); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
