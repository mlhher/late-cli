package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// Premium Palette - Vesper Slate & Amber
	primaryColor   = lipgloss.Color("#E5A85C") // Warm Amber Gold
	secondaryColor = lipgloss.Color("#62B3D5") // Steel Blue
	textColor      = lipgloss.Color("#F3F4F6") // Crisp Off-White
	subtextColor   = lipgloss.Color("#8A94A6") // Muted Slate
	warningColor   = lipgloss.Color("#EF4444") // Red/Orange warning

	// Message Backgrounds
	appBgColor     = lipgloss.Color("#0E0E10") // Charcoal Black
	userMsgBg      = lipgloss.Color("#16161B") // Slightly lighter cool charcoal
	aiMsgBg        = appBgColor                // Keep alias for AI msgs
	thoughtBgColor = lipgloss.Color("#09090A") // Darker Charcoal for thinking

	// Base Style for inheritance
	baseStyle = lipgloss.NewStyle().Background(appBgColor)

	// Layout Constants
	UserMsgOverhead = 6 // MarginL(1) + Border(1) + Padding(2)*2 = 6
	AIMsgOverhead   = 8 // MarginL(1) + Border(1) + PaddingL(4) + PaddingR(2) = 8

	// Styles
	appStyle = baseStyle.Copy().
			Foreground(textColor)

	inputStyle = baseStyle.Copy().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(lipgloss.Color("#232329")).
			BorderBackground(appBgColor).
			MarginBackground(appBgColor).
			Padding(0, 1).
			Height(InputHeight - 1)

	// User Bubble
	userMsgStyle = lipgloss.NewStyle().
			Background(userMsgBg).
			Foreground(textColor).
			Padding(0, 2).
			MarginLeft(1).
			MarginBackground(appBgColor).
			Align(lipgloss.Left).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(secondaryColor).
			BorderBackground(userMsgBg).
			PaddingLeft(2)

	queuedMsgStyle = userMsgStyle.Copy().
			Foreground(subtextColor).
			BorderLeftForeground(subtextColor)

	attachmentStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Italic(true)

	// AI Bubble
	aiMsgStyle = baseStyle.Copy().
			Padding(0, 2).
			MarginLeft(1).
			MarginBackground(appBgColor).
			PaddingLeft(4).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(primaryColor).
			BorderBackground(appBgColor)

	// Thinking Block
	thinkingStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(thoughtBgColor).
			Italic(true).
			Padding(0, 1).
			MarginLeft(4).
			BorderLeft(true).
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("#3F4E5A")).
			BorderBackground(thoughtBgColor)

	tagStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			Background(thoughtBgColor).
			MarginBackground(appBgColor).
			MarginLeft(1).
			PaddingLeft(1)

	thoughtHeaderStyle = tagStyle.Copy().
				Foreground(subtextColor)

	statusBarBaseStyle = lipgloss.NewStyle().
				Background(appBgColor).
				MarginBackground(appBgColor).
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(lipgloss.Color("#232329")).
				BorderBackground(appBgColor).
				Foreground(textColor)

	statusModeStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			MarginRight(1)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Background(appBgColor).
			MarginBackground(appBgColor).
			Bold(true)

	statusTextStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			MarginBackground(appBgColor).
			MarginLeft(1)

	statusWarningStyle = lipgloss.NewStyle().
				Foreground(warningColor).
				Background(appBgColor).
				Bold(true).
				MarginLeft(1)

	keycapStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Bold(true)

	statusAttachedStyle = lipgloss.NewStyle().
				Foreground(secondaryColor).
				Background(appBgColor).
				Bold(true)

	statusTokenStyle = lipgloss.NewStyle().
				Foreground(subtextColor)

	// Breadcrumb styles
	breadcrumbLateStyle = lipgloss.NewStyle().
				Foreground(subtextColor).
				Background(appBgColor)

	breadcrumbSeparatorStyle = lipgloss.NewStyle().
					Foreground(secondaryColor).
					Background(appBgColor)

	breadcrumbAgentStyle = lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor).
				Bold(true)
)
