package main

import (
	"strings"
	"testing"
)

func TestEstimateDifficulty(t *testing.T) {
	tests := []struct {
		name        string
		description string
		expected    int
	}{
		{
			name:        "simple hello world",
			description: "create a hello world program",
			expected:    1,
		},
		{
			name:        "simple test",
			description: "write a simple test for the function",
			expected:    1,
		},
		{
			name:        "API with database",
			description: "build a REST API with user authentication and database",
			expected:    2,
		},
		{
			name:        "microservice with redis",
			description: "create a microservice with redis cache and grpc",
			expected:    3,
		},
		{
			name:        "machine learning",
			description: "implement a deep learning neural network model",
			expected:    4,
		},
		{
			name:        "operating system",
			description: "build a simple operating system kernel",
			expected:    5,
		},
		{
			name:        "consensus algorithm",
			description: "implement a distributed consensus algorithm",
			expected:    5,
		},
		{
			name:        "default simple",
			description: "do something",
			expected:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimateDifficulty(strings.ToLower(tt.description))
			if result != tt.expected {
				t.Errorf("estimateDifficulty(%q) = %d, expected %d", tt.description, result, tt.expected)
			}
		})
	}
}

func TestEstimateHours(t *testing.T) {
	tests := []struct {
		name       string
		desc       string
		difficulty int
		expected   int
	}{
		{"level 1", "simple", 1, 2},
		{"level 2", "api", 2, 8},
		{"level 3", "complex", 3, 24},
		{"level 4", "expert", 4, 80},
		{"level 5", "extreme", 5, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimateHours(tt.desc, tt.difficulty)
			if result != tt.expected {
				t.Errorf("estimateHours() = %d, expected %d", result, tt.expected)
			}
		})
	}
}

func TestIdentifyRisks(t *testing.T) {
	tests := []struct {
		name            string
		desc            string
		expectedMinRisk int // at least this many risks should be identified
	}{
		{
			name:            "node project",
			desc:            "create a node.js application",
			expectedMinRisk: 1,
		},
		{
			name:            "python project",
			desc:            "create a python script",
			expectedMinRisk: 1,
		},
		{
			name:            "third-party API",
			desc:            "integrate with third-party payment api",
			expectedMinRisk: 1,
		},
		{
			name:            "security sensitive",
			desc:            "implement auth with token encryption",
			expectedMinRisk: 1,
		},
		{
			name:            "high performance",
			desc:            "build a high traffic real-time system",
			expectedMinRisk: 1,
		},
		{
			name:            "database migration",
			desc:            "perform database migration with backup",
			expectedMinRisk: 1,
		},
		{
			name:            "no risks",
			desc:            "do a simple thing",
			expectedMinRisk: 1, // "未识别到显著风险" counts
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risks := identifyRisks(strings.ToLower(tt.desc))
			if len(risks) < tt.expectedMinRisk {
				t.Errorf("expected at least %d risks, got %d", tt.expectedMinRisk, len(risks))
			}
		})
	}
}

func TestAssessEnvImpact(t *testing.T) {
	tests := []struct {
		name     string
		desc     string
		expected string
	}{
		{"low impact", "write a function", "low"},
		{"medium impact", "use docker container", "medium"},
		{"high impact", "install a system service", "high"},
		{"medium with database", "use database redis", "medium"},
		{"default low", "do something simple", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := assessEnvImpact(strings.ToLower(tt.desc))
			if result != tt.expected {
				t.Errorf("assessEnvImpact(%q) = %s, expected %s", tt.desc, result, tt.expected)
			}
		})
	}
}

func TestIdentifyApprovalNeeds(t *testing.T) {
	tests := []struct {
		name     string
		desc     string
		expected []string // substrings that should be present
	}{
		{
			name:     "install",
			desc:     "install new dependencies",
			expected: []string{"安装新依赖"},
		},
		{
			name:     "database",
			desc:     "modify database schema",
			expected: []string{"修改数据库结构"},
		},
		{
			name:     "deploy",
			desc:     "deploy to production",
			expected: []string{"部署到服务器"},
		},
		{
			name:     "security",
			desc:     "configure security settings",
			expected: []string{"安全配置"},
		},
		{
			name:     "docker",
			desc:     "use docker",
			expected: []string{"Docker"},
		},
		{
			name:     "no approval",
			desc:     "do a simple thing",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			needs := identifyApprovalNeeds(strings.ToLower(tt.desc))
			for _, exp := range tt.expected {
				found := false
				for _, n := range needs {
					if contains(n, exp) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected approval need containing %q in %v", exp, needs)
				}
			}
			if tt.name == "no approval" && len(needs) > 0 {
				t.Errorf("expected no approval needs, got %v", needs)
			}
		})
	}
}

func TestMakeRecommendation(t *testing.T) {
	tests := []struct {
		name           string
		assessment     Assessment
		expectedResult string
	}{
		{
			name: "simple project",
			assessment: Assessment{
				Difficulty:    1,
				EnvImpact:     "low",
				Risks:         []string{"no risk"},
				NeedsApproval: []string{},
			},
			expectedResult: "proceed",
		},
		{
			name: "complex with high env impact",
			assessment: Assessment{
				Difficulty:    5,
				EnvImpact:     "high",
				Risks:         []string{"risk1"},
				NeedsApproval: []string{},
			},
			expectedResult: "revise",
		},
		{
			name: "complex with many risks",
			assessment: Assessment{
				Difficulty:    4,
				EnvImpact:     "low",
				Risks:         []string{"r1", "r2", "r3", "r4"},
				NeedsApproval: []string{},
			},
			expectedResult: "revise",
		},
		{
			name: "many approvals needed",
			assessment: Assessment{
				Difficulty:    2,
				EnvImpact:     "low",
				Risks:         []string{"r1"},
				NeedsApproval: []string{"a1", "a2", "a3", "a4"},
			},
			expectedResult: "revise",
		},
		{
			name: "moderate complexity",
			assessment: Assessment{
				Difficulty:    3,
				EnvImpact:     "low",
				Risks:         []string{"r1"},
				NeedsApproval: []string{"a1"},
			},
			expectedResult: "proceed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := makeRecommendation(tt.assessment)
			if result != tt.expectedResult {
				t.Errorf("makeRecommendation() = %s, expected %s", result, tt.expectedResult)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
	}

	for _, tt := range tests {
		result := truncateStr(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncateStr(%q, %d) = %q, expected %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestIntMax(t *testing.T) {
	tests := []struct {
		a, b, expected int
	}{
		{1, 2, 2},
		{2, 1, 2},
		{5, 5, 5},
		{0, 0, 0},
		{-1, -2, -1},
	}

	for _, tt := range tests {
		result := intMax(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("intMax(%d, %d) = %d, expected %d", tt.a, tt.b, result, tt.expected)
		}
	}
}

func TestAssessProject(t *testing.T) {
	tests := []struct {
		name        string
		description string
		checks      func(t *testing.T, a Assessment)
	}{
		{
			name:        "simple API",
			description: "build a simple user API with auth",
			checks: func(t *testing.T, a Assessment) {
				if a.Difficulty < 1 {
					t.Error("difficulty should be at least 1")
				}
				if a.EstimatedHours <= 0 {
					t.Error("estimated hours should be positive")
				}
				if len(a.Risks) == 0 {
					t.Error("should identify at least one risk or 'no risk'")
				}
				if a.Recommendation == "" {
					t.Error("recommendation should not be empty")
				}
			},
		},
		{
			name:        "complex system",
			description: "build a distributed system with consensus algorithm",
			checks: func(t *testing.T, a Assessment) {
				if a.Difficulty < 4 {
					t.Errorf("expected high difficulty, got %d", a.Difficulty)
				}
				if a.EstimatedHours < 80 {
					t.Errorf("expected many hours, got %d", a.EstimatedHours)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := assessProject(tt.description)
			tt.checks(t, a)
		})
	}
}

func TestDifficultyBar(t *testing.T) {
	// difficultyBar uses Unicode block characters which are 3 bytes each in UTF-8
	// So a 5-character bar has 15 bytes
	tests := []struct {
		input     int
		expectedRunes int
	}{
		{1, 5},
		{3, 5},
		{5, 5},
		{0, 5},
	}

	for _, tt := range tests {
		result := difficultyBar(tt.input)
		runeCount := 0
		for range result {
			runeCount++
		}
		if runeCount != tt.expectedRunes {
			t.Errorf("difficultyBar(%d) rune count = %d, expected %d", tt.input, runeCount, tt.expectedRunes)
		}
	}
}

func TestEnvImpactLabel(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"low", "低"},
		{"medium", "中"},
		{"high", "高"},
		{"unknown", "未知"},
	}

	for _, tt := range tests {
		result := envImpactLabel(tt.input)
		if !contains(result, tt.contains) {
			t.Errorf("envImpactLabel(%s) = %s, should contain %s", tt.input, result, tt.contains)
		}
	}
}