// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// pciClassNames maps PCI base-class/subclass codes to the names pciutils
// prints without an ids database.
var pciClassNames = map[[2]int]string{
	{0x00, 0x00}: "Non-VGA unclassified device",
	{0x01, 0x00}: "SCSI storage controller",
	{0x01, 0x01}: "IDE interface",
	{0x01, 0x04}: "RAID bus controller",
	{0x01, 0x05}: "ATA controller",
	{0x01, 0x06}: "SATA controller",
	{0x01, 0x07}: "Serial Attached SCSI controller",
	{0x01, 0x08}: "Non-Volatile memory controller",
	{0x02, 0x00}: "Ethernet controller",
	{0x02, 0x01}: "Token ring network controller",
	{0x02, 0x02}: "FDDI network controller",
	{0x02, 0x03}: "ATM network controller",
	{0x02, 0x80}: "Network controller",
	{0x03, 0x00}: "VGA compatible controller",
	{0x03, 0x01}: "XGA compatible controller",
	{0x03, 0x02}: "3D controller",
	{0x03, 0x80}: "Display controller",
	{0x04, 0x00}: "Multimedia video controller",
	{0x04, 0x01}: "Multimedia audio controller",
	{0x04, 0x03}: "Audio device",
	{0x04, 0x80}: "Multimedia controller",
	{0x05, 0x00}: "RAM memory",
	{0x05, 0x80}: "Memory controller",
	{0x06, 0x00}: "Host bridge",
	{0x06, 0x01}: "ISA bridge",
	{0x06, 0x02}: "EISA bridge",
	{0x06, 0x03}: "MCA bridge",
	{0x06, 0x04}: "PCI bridge",
	{0x06, 0x05}: "PCMCIA bridge",
	{0x06, 0x06}: "NuBus bridge",
	{0x06, 0x07}: "CardBus bridge",
	{0x06, 0x08}: "RACEway bridge",
	{0x06, 0x09}: "PCI-to-PCI bridge",
	{0x06, 0x0a}: "InfiniBand-to-PCI host bridge",
	{0x06, 0x80}: "Bridge",
	{0x07, 0x00}: "Serial controller",
	{0x07, 0x01}: "Parallel controller",
	{0x07, 0x03}: "Modem",
	{0x08, 0x00}: "PIC",
	{0x08, 0x01}: "DMA controller",
	{0x08, 0x02}: "Timer",
	{0x08, 0x03}: "RTC controller",
	{0x08, 0x04}: "PCI Hot-plug controller",
	{0x0a, 0x00}: "PIC",
	{0x0b, 0x00}: "Processor",
	{0x0c, 0x00}: "FireWire (IEEE 1394)",
	{0x0c, 0x01}: "Access bus",
	{0x0c, 0x02}: "SSA",
	{0x0c, 0x03}: "USB controller",
	{0x0c, 0x04}: "Fibre Channel",
	{0x0c, 0x05}: "SMBus",
	{0x0c, 0x80}: "Serial bus controller",
}

// cmdLspci implements lspci(1): list PCI devices from sysfs in the original's
// default layout, resolving vendor, device, and class names from pci.ids when
// installed and falling back to a built-in class table. -n prints the numeric
// form instead.
func cmdLspci(args []string) int {
	numeric := false
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "-n", "--numeric":
			numeric = true
		case "--":
			i = len(args)
		default:
			fatalf("lspci", "unsupported option %q", args[i])
			return 1
		}
	}
	db := loadPciIds()
	base := "/sys/bus/pci/devices"
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	type pciDevice struct {
		slot   string
		class  int64
		vendor int64
		device int64
		rev    int64
	}
	var devices []pciDevice
	for _, entry := range entries {
		name := entry.Name()
		dir := filepath.Join(base, name)
		classValue, err1 := readHexFile(filepath.Join(dir, "class"))
		vendor, err2 := readHexFile(filepath.Join(dir, "vendor"))
		deviceID, err3 := readHexFile(filepath.Join(dir, "device"))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		rev, _ := readHexFile(filepath.Join(dir, "revision"))
		slot := name
		if colon := strings.IndexByte(name, ':'); colon > 0 {
			slot = name[colon+1:]
		}
		devices = append(devices, pciDevice{slot: slot, class: classValue, vendor: vendor, device: deviceID, rev: rev})
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].slot < devices[j].slot
	})
	for _, d := range devices {
		if numeric {
			fmt.Printf("%s %04x: %04x:%04x", d.slot, d.class>>8, d.vendor, d.device)
			if d.rev != 0 {
				fmt.Printf(" (rev %02x)", d.rev)
			}
			fmt.Println()
			continue
		}
		class := int((d.class >> 16) & 0xff)
		subclass := int((d.class >> 8) & 0xff)
		className := db.className(class, subclass)
		if className == "" {
			className = pciClassNames[[2]int{class, subclass}]
		}
		if className == "" {
			className = fmt.Sprintf("Class %04x", d.class>>8)
		}
		fmt.Printf("%s %s: ", d.slot, className)
		vendorName := db.vendor(d.vendor)
		deviceName := db.product(d.vendor, d.device)
		switch {
		case vendorName != "" && deviceName != "":
			fmt.Printf("%s %s", vendorName, deviceName)
		case vendorName != "":
			fmt.Printf("%s Device %04x", vendorName, d.device)
		default:
			fmt.Printf("Device %04x:%04x", d.vendor, d.device)
		}
		if d.rev != 0 {
			fmt.Printf(" (rev %02x)", d.rev)
		}
		fmt.Println()
	}
	return 0
}

// pciIds is the parsed pci.ids database: vendor and product names plus class
// names for the C section.
type pciIds struct {
	vendors  map[int64]string
	products map[int64]string
	classes  map[[2]int]string
}

func loadPciIds() *pciIds {
	db := &pciIds{
		vendors:  map[int64]string{},
		products: map[int64]string{},
		classes:  map[[2]int]string{},
	}
	for _, path := range []string{"/usr/share/misc/pci.ids", "/usr/share/hwdata/pci.ids", "/usr/share/pci.ids"} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		var currentVendor int64
		var class, subclass int
		inClass := false
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "C ") {
				parts := strings.SplitN(line[2:], " ", 2)
				if len(parts) == 2 {
					if id, err := strconv.ParseUint(parts[0], 16, 8); err == nil {
						class = int(id)
						subclass = 0
						inClass = true
						db.classes[[2]int{class, subclass}] = strings.TrimSpace(parts[1])
					}
				}
				continue
			}
			if inClass && strings.HasPrefix(line, "\t") {
				if strings.HasPrefix(line, "\t\t") {
					// Prog-if entries; the class section continues.
					continue
				}
				parts := strings.SplitN(strings.TrimLeft(line, "\t"), " ", 2)
				if len(parts) == 2 {
					if id, err := strconv.ParseUint(parts[0], 16, 8); err == nil {
						subclass = int(id)
						db.classes[[2]int{class, subclass}] = strings.TrimSpace(parts[1])
					}
				}
				continue
			}
			inClass = false
			if line[0] != '\t' {
				parts := strings.SplitN(line, " ", 2)
				if len(parts) == 2 {
					if id, err := strconv.ParseInt(parts[0], 16, 64); err == nil {
						currentVendor = id
						db.vendors[id] = strings.TrimSpace(parts[1])
					}
				}
				continue
			}
			trimmed := strings.TrimLeft(line, "\t")
			parts := strings.SplitN(trimmed, " ", 2)
			if len(parts) == 2 {
				if id, err := strconv.ParseInt(parts[0], 16, 64); err == nil {
					db.products[(currentVendor<<16)|id] = strings.TrimSpace(parts[1])
				}
			}
		}
		file.Close()
		break
	}
	return db
}

func (db *pciIds) vendor(id int64) string {
	return db.vendors[id]
}

func (db *pciIds) product(vendor, product int64) string {
	return db.products[(vendor<<16)|product]
}

func (db *pciIds) className(class, subclass int) string {
	if name, ok := db.classes[[2]int{class, subclass}]; ok {
		return name
	}
	if name, ok := db.classes[[2]int{class, 0}]; ok {
		return name
	}
	return ""
}
