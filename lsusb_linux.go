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

// cmdLsusb implements lsusb(1): list the USB devices from sysfs with their
// vendor and product IDs, resolving names from usb.ids when it is installed.
func cmdLsusb(args []string) int {
	for _, a := range args {
		switch a {
		case "--":
		default:
			fatalf("lsusb", "unsupported option %q", a)
			return 1
		}
	}
	db := loadUsbIds()
	base := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	type device struct {
		bus, dev int64
		vendor   int64
		product  int64
	}
	var devices []device
	for _, entry := range entries {
		dir := filepath.Join(base, entry.Name())
		bus, err1 := readHexFile(filepath.Join(dir, "busnum"))
		dev, err2 := readHexFile(filepath.Join(dir, "devnum"))
		vendor, err3 := readHexFile(filepath.Join(dir, "idVendor"))
		product, err4 := readHexFile(filepath.Join(dir, "idProduct"))
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		devices = append(devices, device{bus: bus, dev: dev, vendor: vendor, product: product})
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].bus != devices[j].bus {
			return devices[i].bus < devices[j].bus
		}
		return devices[i].dev < devices[j].dev
	})
	for _, d := range devices {
		vendorName := db.vendor(d.vendor)
		productName := db.product(d.vendor, d.product)
		fmt.Printf("Bus %03d Device %03d: ID %04x:%04x", d.bus, d.dev, d.vendor, d.product)
		if vendorName != "" {
			fmt.Printf(" %s", vendorName)
		}
		if productName != "" {
			fmt.Printf(" %s", productName)
		}
		fmt.Println()
	}
	return 0
}

func readHexFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(data)), "0x"), 16, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// usbIds is the parsed usb.ids database.
type usbIds struct {
	vendors  map[int64]string
	products map[int64]string
}

// loadUsbIds parses usb.ids from the locations usbutils looks at, or returns
// an empty database when none is installed.
func loadUsbIds() *usbIds {
	db := &usbIds{vendors: map[int64]string{}, products: map[int64]string{}}
	for _, path := range []string{
		"/usr/share/hwdata/usb.ids", "/var/lib/usbids/usb.ids",
		"/usr/share/usb.ids", "/usr/share/misc/usb.ids",
	} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		var currentVendor int64
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
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
			if strings.HasPrefix(trimmed, "C ") || strings.HasPrefix(trimmed, "T ") {
				continue
			}
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

func (db *usbIds) vendor(id int64) string {
	return db.vendors[id]
}

func (db *usbIds) product(vendor, product int64) string {
	return db.products[(vendor<<16)|product]
}
