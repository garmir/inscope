package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Config struct {
	scopeFile string
	verbose   bool
	invert    bool
	showStats bool
	strict    bool
}

var config Config

type scopeChecker struct {
	patterns     []*regexp.Regexp
	antipatterns []*regexp.Regexp
	mu           sync.RWMutex
	stats        struct {
		total    int
		inScope  int
		outScope int
	}
}

func init() {
	flag.StringVar(&config.scopeFile, "f", "", "Path to scope file (default: search for .scope)")
	flag.BoolVar(&config.verbose, "v", false, "Verbose output (show pattern matches)")
	flag.BoolVar(&config.invert, "i", false, "Invert results (show out-of-scope items)")
	flag.BoolVar(&config.showStats, "stats", false, "Show statistics at the end")
	flag.BoolVar(&config.strict, "strict", false, "Strict URL parsing (fail on invalid URLs)")
}

func (s *scopeChecker) inScope(input string) (bool, string) {
	s.mu.Lock()
	s.stats.total++
	s.mu.Unlock()

	domain := input
	matchedPattern := ""

	// Extract hostname from URL if needed
	if isURL(input) {
		hostname, err := getHostname(input)
		if err != nil {
			if config.strict {
				return false, ""
			}
			// Fall back to using the input as-is
			domain = input
		} else {
			domain = hostname
		}
	}

	// Normalize domain
	domain = normalizeDomain(domain)

	// Check against patterns
	inScope := false
	for _, p := range s.patterns {
		if p.MatchString(domain) {
			inScope = true
			matchedPattern = p.String()
			break
		}
	}

	// Check against anti-patterns (exclusions)
	for _, p := range s.antipatterns {
		if p.MatchString(domain) {
			s.mu.Lock()
			s.stats.outScope++
			s.mu.Unlock()
			return false, "!" + p.String()
		}
	}

	s.mu.Lock()
	if inScope {
		s.stats.inScope++
	} else {
		s.stats.outScope++
	}
	s.mu.Unlock()

	return inScope, matchedPattern
}

func newScopeChecker(r io.Reader) (*scopeChecker, error) {
	sc := bufio.NewScanner(r)
	s := &scopeChecker{
		patterns:     make([]*regexp.Regexp, 0),
		antipatterns: make([]*regexp.Regexp, 0),
	}

	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		isAnti := false
		if strings.HasPrefix(line, "!") {
			isAnti = true
			line = strings.TrimSpace(line[1:])
		}

		// Convert wildcards to regex if needed
		pattern := convertWildcardToRegex(line)

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid pattern '%s': %w", lineNum, line, err)
		}

		if isAnti {
			s.antipatterns = append(s.antipatterns, re)
		} else {
			s.patterns = append(s.patterns, re)
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("error reading scope file: %w", err)
	}

	if len(s.patterns) == 0 {
		return nil, errors.New("no scope patterns found")
	}

	return s, nil
}

func main() {
	flag.Parse()

	// Open scope file
	sf, err := openScopefile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening scope file: %v\n", err)
		os.Exit(1)
	}
	defer sf.Close()

	// Create scope checker
	checker, err := newScopeChecker(sf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing scope file: %v\n", err)
		os.Exit(1)
	}

	// Process input
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		inScope, pattern := checker.inScope(input)
		
		// Handle inverted results
		if config.invert {
			inScope = !inScope
		}

		if inScope {
			output := input
			if config.verbose && pattern != "" {
				output = fmt.Sprintf("%s [%s]", input, pattern)
			}
			fmt.Println(output)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	// Show statistics if requested
	if config.showStats {
		fmt.Fprintf(os.Stderr, "\nStatistics:\n")
		fmt.Fprintf(os.Stderr, "  Total processed: %d\n", checker.stats.total)
		fmt.Fprintf(os.Stderr, "  In scope:        %d (%.1f%%)\n", 
			checker.stats.inScope, 
			float64(checker.stats.inScope)*100/float64(checker.stats.total))
		fmt.Fprintf(os.Stderr, "  Out of scope:    %d (%.1f%%)\n", 
			checker.stats.outScope,
			float64(checker.stats.outScope)*100/float64(checker.stats.total))
	}
}

func getHostname(s string) (string, error) {
	// Ensure URL has a scheme
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}

	hostname := u.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("no hostname in URL: %s", s)
	}

	return hostname, nil
}

func isURL(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	
	// Check for common URL indicators
	return strings.Contains(s, "://") ||
		strings.HasPrefix(s, "http:") ||
		strings.HasPrefix(s, "https:") ||
		strings.HasPrefix(s, "ftp:") ||
		strings.HasPrefix(s, "ws:") ||
		strings.HasPrefix(s, "wss:") ||
		strings.Contains(s, "/") ||
		strings.Contains(s, "?") ||
		strings.Contains(s, "#")
}

func normalizeDomain(domain string) string {
	// Convert to lowercase
	domain = strings.ToLower(domain)
	
	// Remove trailing dots
	domain = strings.TrimSuffix(domain, ".")
	
	// Remove port if present
	if idx := strings.LastIndex(domain, ":"); idx != -1 {
		// Make sure it's not IPv6
		if !strings.Contains(domain[:idx], ":") && !strings.HasPrefix(domain, "[") {
			domain = domain[:idx]
		}
	}
	
	return domain
}

func convertWildcardToRegex(pattern string) string {
	// If it's already a regex pattern (contains regex metacharacters), return as-is
	if strings.ContainsAny(pattern, "^$[]{}()+?|\\") {
		return pattern
	}

	// Escape dots
	pattern = strings.ReplaceAll(pattern, ".", "\\.")
	
	// Convert wildcards to regex
	pattern = strings.ReplaceAll(pattern, "*", ".*")
	
	// Anchor the pattern
	if !strings.HasPrefix(pattern, "^") {
		pattern = "^" + pattern
	}
	if !strings.HasSuffix(pattern, "$") {
		pattern = pattern + "$"
	}
	
	return pattern
}

func openScopefile() (io.ReadCloser, error) {
	// If scope file is specified, use it
	if config.scopeFile != "" {
		f, err := os.Open(config.scopeFile)
		if err != nil {
			return nil, fmt.Errorf("cannot open scope file '%s': %w", config.scopeFile, err)
		}
		return f, nil
	}

	// Search for .scope file in current and parent directories
	pwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot get working directory: %w", err)
	}

	for {
		scopePath := filepath.Join(pwd, ".scope")
		f, err := os.Open(scopePath)
		if err == nil {
			if config.verbose {
				fmt.Fprintf(os.Stderr, "Using scope file: %s\n", scopePath)
			}
			return f, nil
		}

		// Try parent directory
		parent := filepath.Dir(pwd)
		if parent == pwd {
			break
		}
		pwd = parent
	}

	// Check common locations
	homeDir, err := os.UserHomeDir()
	if err == nil {
		locations := []string{
			filepath.Join(homeDir, ".scope"),
			filepath.Join(homeDir, ".config", "inscope", "scope"),
			"/etc/inscope/scope",
		}
		
		for _, loc := range locations {
			f, err := os.Open(loc)
			if err == nil {
				if config.verbose {
					fmt.Fprintf(os.Stderr, "Using scope file: %s\n", loc)
				}
				return f, nil
			}
		}
	}

	return nil, errors.New("unable to find .scope file (searched current directory, parents, and common locations)")
}