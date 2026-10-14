//go:build darwin

package mactray

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"hpctun/internal/tray"
)

// Run owns the main thread for the life of the process, so the tests run
// on another goroutine while the main goroutine is in AppKit's loop.
func TestMain(m *testing.M) {
	if !windowServer() {
		fmt.Println("mactray: no window server (not in a GUI login session); skipping")
		os.Exit(0)
	}
	go func() { os.Exit(m.Run()) }()
	Run(nil, nil)
}

// windowServer reports whether this process is in a GUI login session; an
// ssh session is not.
func windowServer() bool {
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return false
	}
	var session func() uintptr
	purego.RegisterLibFunc(&session, cg, "CGSessionCopyCurrentDictionary")
	return session() != 0
}

// onMainWait runs f on the main thread and waits for it.
func onMainWait(f func()) {
	done := make(chan struct{})
	onMain(func() { f(); close(done) })
	<-done
}

// goStr reads an NSString.
func goStr(s id) string {
	if s == 0 {
		return ""
	}
	buf := make([]byte, 4096)
	if !objc.Send[bool](s, sel("getCString:maxLength:encoding:"), &buf[0], len(buf), 4) { // NSUTF8StringEncoding
		return "<unreadable>"
	}
	return string(buf[:bytes.IndexByte(buf, 0)])
}

type row struct {
	title           string
	enabled, hidden bool
	state           int
	tooltip         string
}

// read returns what AppKit shows for it.
func read(it *MenuItem) (r row) {
	onMainWait(func() {
		n := it.nsItem()
		r = row{
			title:   goStr(n.Send(sel("title"))),
			enabled: objc.Send[bool](n, sel("isEnabled")),
			hidden:  objc.Send[bool](n, sel("isHidden")),
			state:   objc.Send[int](n, sel("state")),
			tooltip: goStr(n.Send(sel("toolTip"))),
		}
	})
	return r
}

func indexOf(it *MenuItem) (i int) {
	onMainWait(func() { i = objc.Send[int](menu, sel("indexOfItem:"), it.nsItem()) })
	return i
}

func TestNewItems(t *testing.T) {
	plain := AddMenuItem("plain", "a hint")
	box := AddMenuItemCheckbox("box", "", true)
	if got, want := read(plain), (row{"plain", true, false, 0, "a hint"}); got != want {
		t.Errorf("plain: %+v, want %+v", got, want)
	}
	if got, want := read(box), (row{"box", true, false, 1, ""}); got != want {
		t.Errorf("box: %+v, want %+v", got, want)
	}
	if !box.Checked() || plain.Checked() {
		t.Errorf("Checked: box %v, plain %v", box.Checked(), plain.Checked())
	}
}

func TestSettersFromAnotherGoroutineApplyInOrder(t *testing.T) {
	it := AddMenuItemCheckbox("first", "", false)
	done := make(chan struct{})
	go func() {
		it.SetTitle("second")
		it.SetTitle("third")
		it.Hide()
		it.Show()
		it.Disable()
		it.Check()
		close(done)
	}()
	<-done
	if !it.Checked() {
		t.Error("Checked() = false after Check")
	}
	if got, want := read(it), (row{"third", false, false, 1, ""}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	it.Uncheck()
	it.Enable()
	it.Hide()
	if got, want := read(it), (row{"third", true, true, 0, ""}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if it.Checked() {
		t.Error("Checked() = true after Uncheck")
	}
}

func TestSeparator(t *testing.T) {
	above := AddMenuItem("above", "")
	AddSeparator()
	below := AddMenuItem("below", "")
	a, b := indexOf(above), indexOf(below)
	var sep bool
	onMainWait(func() { sep = objc.Send[bool](menu.Send(sel("itemAtIndex:"), a+1), sel("isSeparatorItem")) })
	if b != a+2 || !sep {
		t.Errorf("above at %d, below at %d, separator between: %v", a, b, sep)
	}
}

// clickItem clicks it n times through AppKit's own action path.
func clickItem(it *MenuItem, n int) {
	i := indexOf(it)
	onMainWait(func() {
		for range n {
			menu.Send(sel("performActionForItemAtIndex:"), i)
		}
	})
}

func TestClickReachesAWaitingReader(t *testing.T) {
	it := AddMenuItem("click me", "")
	got := make(chan struct{})
	go func() { <-it.ClickedCh; close(got) }()
	// The reader may not be waiting yet; click until it has the click.
	for deadline := time.Now().Add(2 * time.Second); ; {
		clickItem(it, 1)
		select {
		case <-got:
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the click did not reach ClickedCh")
		}
	}
}

// As in fyne.io/systray, a click while nobody reads ClickedCh (the view
// is still handling the last one) is dropped, not delivered later: a
// double click on a tunnel must not open it and then close it again.
func TestClickWithNobodyReadingIsDropped(t *testing.T) {
	it := AddMenuItem("busy", "")
	clickItem(it, 2)
	select {
	case <-it.ClickedCh:
		t.Error("a click made while nobody was reading was delivered later")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestTemplateIconAndTooltip(t *testing.T) {
	tpl, reg := tray.Icons(tray.IconOff)
	SetTemplateIcon(tpl, reg)
	SetTooltip("hpctun: test")
	var template bool
	var tip string
	onMainWait(func() {
		img := button.Send(sel("image"))
		template = img != 0 && objc.Send[bool](img, sel("isTemplate"))
		tip = goStr(button.Send(sel("toolTip")))
	})
	if !template || tip != "hpctun: test" {
		t.Errorf("template image %v, tooltip %q", template, tip)
	}
}

// Setters run on whatever OS thread their goroutine is on. Anything
// Cocoa autoreleases there without a pool leaks, so a day of status polls
// would grow the tray. The runtime reports each such leak when
// OBJC_DEBUG_MISSING_POOLS is set. AppKit's own start-up reports some once,
// so compare a child that sets things once with one that does it 300
// times: the counts must be the same.
func TestSettersDoNotLeak(t *testing.T) {
	if os.Getenv("MACTRAY_POOL_ROUNDS") != "" {
		t.Skip("in a child run")
	}
	missing := func(rounds string) int {
		cmd := exec.Command(os.Args[0], "-test.count=1", "-test.run=^TestPoolChild$")
		cmd.Env = append(os.Environ(), "MACTRAY_POOL_ROUNDS="+rounds, "OBJC_DEBUG_MISSING_POOLS=YES")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child run: %v\n%s", err, out)
		}
		return strings.Count(string(out), "MISSING POOLS")
	}
	if once, many := missing("1"), missing("300"); many != once {
		t.Errorf("objects autoreleased with no pool: %d after 1 round of setters, %d after 300", once, many)
	}
}

// TestPoolChild is TestSettersDoNotLeak's child: rounds of setters from
// other goroutines, as the tray's polls make them.
func TestPoolChild(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("MACTRAY_POOL_ROUNDS"))
	if n == 0 {
		t.Skip("run by TestSettersDoNotLeak")
	}
	it := AddMenuItemCheckbox("polled", "", false)
	tpl, reg := tray.Icons(tray.IconOff)
	for i := range n {
		done := make(chan struct{})
		go func() {
			it.SetTitle(fmt.Sprint("polled ", i))
			it.Check()
			it.Hide()
			it.Show()
			SetTooltip(fmt.Sprint("tooltip ", i))
			SetTemplateIcon(tpl, reg)
			close(done)
		}()
		<-done
	}
	onMainWait(func() {})
}
