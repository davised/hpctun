package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

// pageAlign is log2 of the alignment lipo gives a slice of each CPU: its
// page size.
func pageAlign(cpu macho.Cpu) (uint32, error) {
	switch cpu {
	case macho.CpuAmd64:
		return 12, nil
	case macho.CpuArm64:
		return 14, nil
	}
	return 0, fmt.Errorf("no release is built for CPU %v", cpu)
}

// makeFat returns a universal Mach-O file holding the thin Mach-O files at
// paths, laid out as lipo -create lays it out: a fat_header, one fat_arch
// per slice, and the slices sorted by alignment, each at an offset aligned
// to its own page size. Code signatures are per slice, so signed slices
// stay signed.
func makeFat(paths ...string) ([]byte, error) {
	type slice struct {
		cpu   macho.Cpu
		sub   uint32
		align uint32
		data  []byte
	}
	var slices []slice
	seen := map[macho.Cpu]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s: not a thin Mach-O file: %w", p, err)
		}
		cpu, sub := f.Cpu, f.SubCpu
		f.Close()
		if prev, ok := seen[cpu]; ok {
			return nil, fmt.Errorf("%s: a second %v slice (the first is %s)", p, cpu, prev)
		}
		seen[cpu] = p
		align, err := pageAlign(cpu)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		slices = append(slices, slice{cpu, sub, align, data})
	}
	if len(slices) < 2 {
		return nil, fmt.Errorf("need two thin Mach-O files, got %d", len(slices))
	}
	sort.SliceStable(slices, func(i, j int) bool { return slices[i].align < slices[j].align })

	out := make([]byte, 8+20*len(slices))
	binary.BigEndian.PutUint32(out[0:], macho.MagicFat)
	binary.BigEndian.PutUint32(out[4:], uint32(len(slices)))
	for i, s := range slices {
		if pad := (1<<s.align - len(out)%(1<<s.align)) % (1 << s.align); pad > 0 {
			out = append(out, make([]byte, pad)...)
		}
		e := 8 + 20*i
		binary.BigEndian.PutUint32(out[e:], uint32(s.cpu))
		binary.BigEndian.PutUint32(out[e+4:], s.sub)
		binary.BigEndian.PutUint32(out[e+8:], uint32(len(out)))
		binary.BigEndian.PutUint32(out[e+12:], uint32(len(s.data)))
		binary.BigEndian.PutUint32(out[e+16:], s.align)
		out = append(out, s.data...)
	}
	return out, nil
}
