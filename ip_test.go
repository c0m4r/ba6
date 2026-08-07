// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"net"
	"syscall"
	"testing"
)

func TestParseIPv4Route(t *testing.T) {
	spec, err := parseRouteSpec(syscall.AF_UNSPEC, []string{"192.0.2.0/24", "via", "192.0.2.1", "dev", "eth0", "metric", "10"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.family != syscall.AF_INET || spec.prefix != 24 || !spec.dst.Equal(net.ParseIP("192.0.2.0")) ||
		!spec.via.Equal(net.ParseIP("192.0.2.1")) || spec.dev != "eth0" || spec.metric == nil || *spec.metric != 10 {
		t.Fatalf("unexpected route spec: %+v", spec)
	}
}

func TestParseIPv6DefaultRoute(t *testing.T) {
	spec, err := parseRouteSpec(syscall.AF_UNSPEC, []string{"default", "via", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.family != syscall.AF_INET6 || spec.prefix != 0 {
		t.Fatalf("unexpected route spec: %+v", spec)
	}
}

func TestParseRouteRejectsMismatchedFamilies(t *testing.T) {
	if _, err := parseRouteSpec(syscall.AF_UNSPEC, []string{"192.0.2.0/24", "via", "2001:db8::1"}); err == nil {
		t.Fatal("mixed address families were accepted")
	}
}

func TestParseBareRouteForDeletion(t *testing.T) {
	spec, err := parseRouteSpec(syscall.AF_UNSPEC, []string{"192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.via != nil || spec.dev != "" {
		t.Fatalf("unexpected next hop: %+v", spec)
	}
}

func TestNetlinkAttributeAlignment(t *testing.T) {
	attr := netlinkAttribute(syscall.RTA_DST, []byte{1, 2, 3, 4, 5})
	if len(attr) != 12 {
		t.Fatalf("attribute length is %d", len(attr))
	}
}
