//go:build !windows

package main

func readRunAsAdminSetting(string) (bool, error) { return false, nil }

func writeRunAsAdminSetting(string, bool) error { return nil }

func readAutoStartSetting() (bool, error) { return false, nil }

func writeAutoStartSetting(string, bool) error { return nil }
