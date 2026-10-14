//go:build darwin

package mactray

import (
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AppKit may only be used on the main thread, and Run is called from the
// main goroutine: keep that goroutine on the main OS thread.
func init() { runtime.LockOSThread() }

type id = objc.ID

var sel = objc.RegisterName

var (
	selNew, selAlloc, selRelease = sel("new"), sel("alloc"), sel("release")
	selRetain, selDrain          = sel("retain"), sel("drain")
	selPerform                   = sel("performSelectorOnMainThread:withObject:waitUntilDone:")
	selRunQueued, selClicked     = sel("runQueued:"), sel("itemClicked:")
	selTag, selSetTag            = sel("tag"), sel("setTag:")
	selSetState, selSetEnabled   = sel("setState:"), sel("setEnabled:")
	selSetHidden, selSetTitle    = sel("setHidden:"), sel("setTitle:")
	selSetTarget, selSetToolTip  = sel("setTarget:"), sel("setToolTip:")
	selAddItem, selSetImage      = sel("addItem:"), sel("setImage:")
	selUTF8String                = sel("stringWithUTF8String:")
)

var (
	// Set by Run on the main thread and used only there.
	app, menu, button id
	onExitFn          func()

	qmu    sync.Mutex
	target id // receives runQueued: and every item's action; set by Run
	queue  []func()

	imu   sync.Mutex
	items []*MenuItem // by tag
)

func class(name string) id { return id(objc.GetClass(name)) }

// nsString returns an autoreleased NSString holding s.
func nsString(s string) id {
	b := append([]byte(s), 0)
	str := class("NSString").Send(selUTF8String, &b[0])
	runtime.KeepAlive(b)
	return str
}

// onMain queues f to run on the main thread and returns at once. Queued
// functions run in order. performSelectorOnMainThread runs in the run
// loop's common modes, so this works while the menu is open too.
func onMain(f func()) {
	qmu.Lock()
	queue = append(queue, f)
	t := target
	qmu.Unlock()
	if t != 0 {
		t.Send(selPerform, selRunQueued, id(0), false)
	}
}

// runQueued runs everything queued so far, inside an autorelease pool so
// the strings and images made for it are freed.
func runQueued(_ id, _ objc.SEL, _ id) {
	pool := class("NSAutoreleasePool").Send(selNew)
	defer pool.Send(selDrain)
	qmu.Lock()
	q := queue
	queue = nil
	qmu.Unlock()
	for _, f := range q {
		f()
	}
}

// itemClicked is every item's action: it tells the item's ClickedCh when
// someone is reading it and drops the click otherwise, as systray does.
func itemClicked(_ id, _ objc.SEL, sender id) {
	tag := objc.Send[int](sender, selTag)
	imu.Lock()
	var it *MenuItem
	if tag >= 0 && tag < len(items) {
		it = items[tag]
	}
	imu.Unlock()
	if it != nil {
		select {
		case it.ClickedCh <- struct{}{}:
		default:
		}
	}
}

// Run shows the status item, calls onReady, and runs AppKit's loop until
// Quit. Call it from the main goroutine. It panics if AppKit cannot be
// set up: a menu-bar program has no other way to say so.
func Run(onReady, onExit func()) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		panic("mactray: loading AppKit: " + err.Error())
	}
	onExitFn = onExit
	pool := class("NSAutoreleasePool").Send(selNew)
	tc, err := objc.RegisterClass("HpctunTrayTarget", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: selRunQueued, Fn: runQueued},
		{Cmd: selClicked, Fn: itemClicked},
	})
	if err != nil {
		panic("mactray: registering the menu's target class: " + err.Error())
	}
	app = class("NSApplication").Send(sel("sharedApplication"))
	// Accessory: no Dock icon, even outside a bundle.
	app.Send(sel("setActivationPolicy:"), 1)
	// -1 is NSVariableStatusItemLength.
	status := class("NSStatusBar").Send(sel("systemStatusBar")).Send(sel("statusItemWithLength:"), float64(-1))
	status.Send(selRetain)
	button = status.Send(sel("button"))
	menu = class("NSMenu").Send(selNew)
	menu.Send(sel("setAutoenablesItems:"), false) // items are enabled by Enable and Disable only
	status.Send(sel("setMenu:"), menu)

	t := id(tc).Send(selNew)
	qmu.Lock()
	target = t
	qmu.Unlock()
	if onReady != nil {
		onReady()
	}
	// What was queued before target existed runs once the loop starts.
	t.Send(selPerform, selRunQueued, id(0), false)
	pool.Send(selDrain)
	app.Send(sel("run"))
}

// Quit runs onExit and ends the process.
func Quit() {
	onMain(func() {
		if onExitFn != nil {
			onExitFn()
		}
		app.Send(sel("terminate:"), id(0))
	})
}

type nsSize struct{ W, H float64 }

// SetTemplateIcon shows templatePNG (black where the shape is, transparent
// elsewhere) as a template image, which macOS tints for light and dark
// menu bars. The regular icon is not used on macOS, as in systray.
func SetTemplateIcon(templatePNG, _ []byte) {
	if len(templatePNG) == 0 {
		return
	}
	data := append([]byte(nil), templatePNG...)
	onMain(func() {
		d := class("NSData").Send(sel("dataWithBytes:length:"), unsafe.Pointer(&data[0]), uint(len(data)))
		img := class("NSImage").Send(selAlloc).Send(sel("initWithData:"), d)
		runtime.KeepAlive(data)
		if img == 0 {
			return
		}
		img.Send(sel("setTemplate:"), true)
		img.Send(sel("setSize:"), nsSize{18, 18})
		button.Send(selSetImage, img)
		img.Send(selRelease)
	})
}

// SetTooltip sets the status item's tooltip.
func SetTooltip(s string) { onMain(func() { button.Send(selSetToolTip, nsString(s)) }) }

// MenuItem is one row of the menu. A click sends on ClickedCh if someone
// is reading it at that moment.
type MenuItem struct {
	ClickedCh chan struct{}

	mu      sync.Mutex
	ns      id // the NSMenuItem, set on the main thread
	checked bool
}

func add(title, tooltip string, checked bool) *MenuItem {
	it := &MenuItem{ClickedCh: make(chan struct{}), checked: checked}
	imu.Lock()
	tag := len(items)
	items = append(items, it)
	imu.Unlock()
	onMain(func() {
		n := class("NSMenuItem").Send(selAlloc).Send(sel("initWithTitle:action:keyEquivalent:"), nsString(title), selClicked, nsString(""))
		n.Send(selSetTarget, target)
		n.Send(selSetTag, tag)
		if tooltip != "" {
			n.Send(selSetToolTip, nsString(tooltip))
		}
		if checked {
			n.Send(selSetState, 1)
		}
		menu.Send(selAddItem, n)
		it.mu.Lock()
		it.ns = n
		it.mu.Unlock()
	})
	return it
}

// AddMenuItem adds a row at the end of the menu.
func AddMenuItem(title, tooltip string) *MenuItem { return add(title, tooltip, false) }

// AddMenuItemCheckbox adds a row that shows a check mark when checked.
func AddMenuItemCheckbox(title, tooltip string, checked bool) *MenuItem {
	return add(title, tooltip, checked)
}

// AddSeparator adds a line at the end of the menu.
func AddSeparator() {
	onMain(func() { menu.Send(selAddItem, class("NSMenuItem").Send(sel("separatorItem"))) })
}

func (it *MenuItem) nsItem() id {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.ns
}

// do runs f with the item's NSMenuItem on the main thread.
func (it *MenuItem) do(f func(n id)) {
	onMain(func() {
		if n := it.nsItem(); n != 0 {
			f(n)
		}
	})
}

func (it *MenuItem) SetTitle(s string) { it.do(func(n id) { n.Send(selSetTitle, nsString(s)) }) }
func (it *MenuItem) Enable()           { it.do(func(n id) { n.Send(selSetEnabled, true) }) }
func (it *MenuItem) Disable()          { it.do(func(n id) { n.Send(selSetEnabled, false) }) }
func (it *MenuItem) Show()             { it.do(func(n id) { n.Send(selSetHidden, false) }) }
func (it *MenuItem) Hide()             { it.do(func(n id) { n.Send(selSetHidden, true) }) }
func (it *MenuItem) Check()            { it.setChecked(true) }
func (it *MenuItem) Uncheck()          { it.setChecked(false) }

// Checked reports the state last set; AppKit does not change it on a
// click.
func (it *MenuItem) Checked() bool {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.checked
}

func (it *MenuItem) setChecked(on bool) {
	it.mu.Lock()
	it.checked = on
	it.mu.Unlock()
	state := 0
	if on {
		state = 1
	}
	it.do(func(n id) { n.Send(selSetState, state) })
}
