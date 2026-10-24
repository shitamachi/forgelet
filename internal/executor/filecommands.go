package executor

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const maxCommandFile = 1 << 20

func readCommandFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect file command: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("file command must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file command: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxCommandFile+1))
	if err != nil {
		return "", fmt.Errorf("read file command: %w", err)
	}
	if len(data) > maxCommandFile || strings.ContainsRune(string(data), 0) {
		return "", fmt.Errorf("file command exceeds size limit or contains NUL")
	}
	return string(data), nil
}

func applyFileCommands(files map[string]string, env map[string]string) (map[string]string, error) {
	envText, err := readCommandFile(files["ENV"])
	if err != nil {
		return nil, err
	}
	changes, err := parseCommandPairs(envText)
	if err != nil {
		return nil, err
	}
	for key, value := range changes {
		if err := validateEnvKey(key); err != nil {
			return nil, fmt.Errorf("GITHUB_ENV: %w", err)
		}
		env[key] = value
	}
	pathText, err := readCommandFile(files["PATH"])
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(pathText, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			env["PATH"] = line + string(os.PathListSeparator) + env["PATH"]
		}
	}
	outputText, err := readCommandFile(files["OUTPUT"])
	if err != nil {
		return nil, err
	}
	return parseCommandPairs(outputText)
}

func parseCommandPairs(text string) (map[string]string, error) {
	result := make(map[string]string)
	lines := strings.Split(text, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		if line == "" {
			continue
		}
		equal, heredoc := strings.Index(line, "="), strings.Index(line, "<<")
		var key, value string
		if equal >= 0 && (heredoc < 0 || equal < heredoc) {
			key, value, _ = strings.Cut(line, "=")
		} else if heredoc > 0 {
			key = line[:heredoc]
			delimiter := line[heredoc+2:]
			if delimiter == "" {
				return nil, fmt.Errorf("file command heredoc delimiter is empty")
			}
			start := index + 1
			index++
			for index < len(lines) && strings.TrimSuffix(lines[index], "\r") != delimiter {
				index++
			}
			if index == len(lines) {
				return nil, fmt.Errorf("file command heredoc is unterminated")
			}
			parts := append([]string(nil), lines[start:index]...)
			for i := range parts {
				parts[i] = strings.TrimSuffix(parts[i], "\r")
			}
			value = strings.Join(parts, "\n")
		} else {
			return nil, fmt.Errorf("invalid file command entry")
		}
		if !outputKey.MatchString(key) {
			return nil, fmt.Errorf("invalid file command key")
		}
		result[key] = value
	}
	return result, nil
}
