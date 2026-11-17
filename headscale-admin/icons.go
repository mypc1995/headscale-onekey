package main

import (
	"html/template"
	"strconv"
)

// 内联 Lucide 风格线性图标，避免依赖外部 CDN，内网离线也能显示。
var iconPaths = map[string]string{
	"grid":     `<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/>`,
	"users":    `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`,
	"cpu":      `<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/>`,
	"key":      `<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>`,
	"network":  `<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v3M12 8v4"/>`,
	"activity": `<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>`,
	"shield":   `<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>`,
	"palette":  `<path d="M12 2C6.5 2 2 6.1 2 11.2c0 4.5 3.4 8.2 7.7 8.2 1.4 0 2.3-.8 2.3-1.9 0-.6-.2-1-.6-1.4-.3-.4-.5-.8-.5-1.3 0-1.1.9-2 2-2h1.6c3.1 0 5.5-2.6 5.5-5.8C20 6.1 16.6 2 12 2Z"/><circle cx="7.5" cy="11" r="1" fill="currentColor" stroke="none"/><circle cx="10" cy="6.5" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="7" r="1" fill="currentColor" stroke="none"/><circle cx="17.5" cy="11" r="1" fill="currentColor" stroke="none"/>`,
	"globe":    `<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>`,
	"menu":     `<path d="M4 6h16M4 12h16M4 18h16"/>`,
	"logout":   `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"trash":    `<path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6"/>`,
	"edit":     `<path d="M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>`,
	"clock":    `<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`,
	"refresh":  `<path d="M21 12a9 9 0 1 1-3-6.7L21 8M21 3v5h-5"/>`,
	"check":    `<path d="m20 6-11 11-5-5"/>`,
	"x":        `<path d="M18 6 6 18M6 6l12 12"/>`,
	"chevron":  `<path d="m6 9 6 6 6-6"/>`,
	"terminal": `<path d="m4 17 6-5-6-5M12 19h8"/>`,
	"database": `<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.66 4.03 3 9 3s9-1.34 9-3V5M3 12c0 1.66 4.03 3 9 3s9-1.34 9-3"/>`,
	"lock":     `<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`,
	"tag":      `<path d="M20.6 13.4 12 22l-9-9V4a1 1 0 0 1 1-1h8.5l8.1 8.1a2 2 0 0 1 0 2.3Z"/><circle cx="7.5" cy="7.5" r="1.2" fill="currentColor" stroke="none"/>`,
	"route":    `<circle cx="6" cy="19" r="3"/><circle cx="18" cy="5" r="3"/><path d="M6 16V9a3 3 0 0 1 3-3h6M15 19h3a3 3 0 0 0 3-3v-4"/>`,
	"server":   `<rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><path d="M6 7h.01M6 17h.01"/>`,
	"file":     `<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>`,
	"save":     `<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/>`,
	"bolt":     `<path d="M13 2 3 14h9l-1 8 10-12h-9l1-8Z"/>`,
	"copy":     `<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`,
}

func icon(name string, size int) template.HTML {
	p, ok := iconPaths[name]
	if !ok {
		p = iconPaths["grid"]
	}
	s := strconv.Itoa(size)
	return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="` + s + `" height="` + s + `" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + p + `</svg>`)
}
