package main

import (
	"bufio"
	"os"
	"strings"
)

func loadEnv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		result[key] = value
	}
	return result, scanner.Err()
}

func resolveEnv(envPath string) (string, string, error) {
	appID := os.Getenv("APPID")
	secret := os.Getenv("SECRET")
	if appID == "" || secret == "" {
		fromFile, err := loadEnv(envPath)
		if err != nil && !os.IsNotExist(err) {
			return "", "", err
		}
		if appID == "" {
			appID = fromFile["APPID"]
		}
		if secret == "" {
			secret = fromFile["SECRET"]
		}
	}
	return appID, secret, nil
}
