package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// urlRe acha links no meio de qualquer texto (colado, .txt, atalho .url...).
var urlRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// extractURLs devolve todos os links http(s) do texto, sem repetir.
func extractURLs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range urlRe.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;)]}")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

var timeRe = regexp.MustCompile(`^\d+(:\d{1,2}){0,2}(\.\d+)?$`)

// toSeconds converte "90", "1:30" ou "1:02:03" em segundos.
func toSeconds(s string) float64 {
	total := 0.0
	for _, p := range strings.Split(s, ":") {
		v, _ := strconv.ParseFloat(p, 64)
		total = total*60 + v
	}
	return total
}

// sectionArg monta o valor de --download-sections ("*1:30-5:00").
// Devolve "" quando o recurso está desligado.
func sectionArg(on bool, from, to string) (string, error) {
	if !on {
		return "", nil
	}
	from = strings.TrimSpace(from)
	to = strings.ToLower(strings.TrimSpace(to))
	if from == "" {
		from = "0"
	}
	if to == "" || to == "fim" || to == "end" || to == "inf" {
		to = "inf"
	}

	if !timeRe.MatchString(from) {
		return "", fmt.Errorf("O tempo inicial \"%s\" não é válido.\n\nUse segundos (90) ou minutos:segundos (1:30) ou h:mm:ss (1:02:03).", from)
	}
	if to != "inf" {
		if !timeRe.MatchString(to) {
			return "", fmt.Errorf("O tempo final \"%s\" não é válido.\n\nUse segundos (90), minutos:segundos (5:00), h:mm:ss, ou deixe vazio para ir até o fim.", to)
		}
		if toSeconds(to) <= toSeconds(from) {
			return "", fmt.Errorf("O tempo final precisa ser maior que o inicial.")
		}
	}
	return "*" + from + "-" + to, nil
}
