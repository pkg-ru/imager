package imagemagick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyXML_DenyByDefault(t *testing.T) {
	data, err := policyXML(PolicyConfig{Enabled: true, DisableNetwork: true})
	if err != nil {
		t.Fatalf("policyXML: %v", err)
	}
	s := string(data)
	// Deny-by-default для coders и delegates.
	if !strings.Contains(s, `<policy domain="coder" rights="none" pattern="*"/>`) {
		t.Error("missing deny-by-default coder policy")
	}
	if !strings.Contains(s, `<policy domain="delegate" rights="none" pattern="*"/>`) {
		t.Error("missing deny-by-default delegate policy")
	}
	// Whitelist безопасных coders.
	if !strings.Contains(s, `<policy domain="coder" rights="read|write" pattern="PNG"/>`) {
		t.Error("missing PNG allow policy")
	}
	// Network-capable delegates запрещены.
	if !strings.Contains(s, `<policy domain="delegate" rights="none" pattern="https"/>`) {
		t.Error("missing https delegate deny")
	}
	// Опасные coders запрещены.
	if !strings.Contains(s, `<policy domain="coder" rights="none" pattern="MSL"/>`) {
		t.Error("missing MSL coder deny")
	}
	// Никакого shell execution.
	if strings.Contains(s, "exec") || strings.Contains(s, "system(") {
		t.Error("policy must not contain shell execution")
	}
}

func TestPolicyXML_ResourceLimits(t *testing.T) {
	data, err := policyXML(PolicyConfig{
		Enabled:        true,
		MaxMemoryBytes: 1 << 30,
		MaxThreads:     4,
		MaxWidth:       1000000,
		MaxHeight:      1000000,
		MaxPixels:      1000000,
		MaxFrames:      10,
	})
	if err != nil {
		t.Fatalf("policyXML: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `name="memory" value="1073741824"`) {
		t.Error("missing memory resource policy")
	}
	if !strings.Contains(s, `name="thread" value="4"`) {
		t.Error("missing thread resource policy")
	}
	if !strings.Contains(s, `name="width" value="1000000"`) {
		t.Error("missing width resource policy")
	}
	if !strings.Contains(s, `name="height" value="1000000"`) {
		t.Error("missing height resource policy")
	}
	// C2: area — лимит площади (защита от bomb).
	if !strings.Contains(s, `name="area" value="1000000"`) {
		t.Error("missing area resource policy")
	}
	if !strings.Contains(s, `name="list-length" value="10"`) {
		t.Error("missing list-length resource policy")
	}
}

func TestPolicyXML_SVGDenied(t *testing.T) {
	data, err := policyXML(PolicyConfig{Enabled: true, DisableNetwork: true})
	if err != nil {
		t.Fatalf("policyXML: %v", err)
	}
	s := string(data)
	// C6: SVG запрещён явно (dangerousCoders) и не в whitelist.
	if !strings.Contains(s, `<policy domain="coder" rights="none" pattern="SVG"/>`) {
		t.Error("missing SVG coder deny")
	}
	if strings.Contains(s, `pattern="SVG"/>`) && strings.Contains(s, `rights="read|write" pattern="SVG"`) {
		t.Error("SVG should not be in safe whitelist")
	}
	// rsvg/inkscape delegates запрещены.
	if !strings.Contains(s, `<policy domain="delegate" rights="none" pattern="rsvg"/>`) {
		t.Error("missing rsvg delegate deny")
	}
	if !strings.Contains(s, `<policy domain="delegate" rights="none" pattern="inkscape"/>`) {
		t.Error("missing inkscape delegate deny")
	}
}

func TestWritePolicyXML_Disabled(t *testing.T) {
	dir, err := writePolicyXML(PolicyConfig{Enabled: false})
	if err != nil {
		t.Fatalf("writePolicyXML: %v", err)
	}
	if dir != "" {
		t.Errorf("disabled policy should return empty dir, got %q", dir)
	}
}

func TestWritePolicyXML_Enabled(t *testing.T) {
	dir, err := writePolicyXML(PolicyConfig{Enabled: true})
	if err != nil {
		t.Fatalf("writePolicyXML: %v", err)
	}
	if dir == "" {
		t.Fatal("expected non-empty dir")
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(filepath.Join(dir, "policy.xml"))
	if err != nil {
		t.Fatalf("read policy.xml: %v", err)
	}
	if !strings.Contains(string(data), "<policymap>") {
		t.Error("policy.xml missing policymap")
	}
}

func TestWritePolicyXML_CustomDir(t *testing.T) {
	dir := t.TempDir()
	got, err := writePolicyXML(PolicyConfig{Enabled: true, Dir: dir})
	if err != nil {
		t.Fatalf("writePolicyXML: %v", err)
	}
	if got != dir {
		t.Errorf("got dir %q, want %q", got, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "policy.xml")); err != nil {
		t.Fatalf("policy.xml not written: %v", err)
	}
}
