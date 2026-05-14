package strategy

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Drop struct {
	X, Y       int
	Troop      string
	Quantity   int
	Delay      int
	Slot       int
	DropType   string
	Wave       int
}

type AttackStrategy struct {
	Name        string
	Description string
	DeployLine  string
	Sides       int
	DropOrders  []Drop
}

func ParseCSV(path string) (*AttackStrategy, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer file.Close()

	var name, description, deployLine string
	var sides int
	var drops []Drop

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 1 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(parts[0]))

		switch {
		case strings.HasPrefix(key, "attackplan"):
			if len(parts) >= 3 {
				name = strings.Trim(parts[1], " \";")
				description = strings.Trim(parts[2], " \";")
			}

		case strings.HasPrefix(key, "deployingtype"):
			deployLine = strings.Trim(strings.Join(parts[1:], ","), " \";")
			if strings.Contains(deployLine, "8 sides") {
				sides = 8
			} else if strings.Contains(deployLine, "4 sides") {
				sides = 4
			}

		case key == "troop" || key == "unit":
			if len(parts) >= 5 {
				x, _ := strconv.Atoi(strings.Trim(parts[1], " "))
				y, _ := strconv.Atoi(strings.Trim(parts[2], " "))
				troop := strings.Trim(parts[3], " \";")
				qty, _ := strconv.Atoi(strings.Trim(parts[4], " \";"))
				drops = append(drops, Drop{X: x, Y: y, Troop: troop, Quantity: qty})
			}

		case strings.HasPrefix(key, "wave"):
			if len(parts) >= 6 {
				x, _ := strconv.Atoi(strings.Trim(parts[2], " "))
				y, _ := strconv.Atoi(strings.Trim(parts[3], " "))
				troop := strings.Trim(parts[4], " \";")
				qty, _ := strconv.Atoi(strings.Trim(parts[5], " \";"))
				drops = append(drops, Drop{X: x, Y: y, Troop: troop, Quantity: qty})
			}
		}
	}

	return &AttackStrategy{
		Name:        name,
		Description: description,
		DeployLine:  deployLine,
		Sides:       sides,
		DropOrders:  drops,
	}, nil
}

func (s AttackStrategy) TroopCount(troop string) int {
	var total int
	for _, d := range s.DropOrders {
		if strings.EqualFold(d.Troop, troop) {
			total += d.Quantity
		}
	}
	return total
}

func (s AttackStrategy) AllTroops() []string {
	seen := make(map[string]bool)
	var troops []string
	for _, d := range s.DropOrders {
		if !seen[d.Troop] {
			seen[d.Troop] = true
			troops = append(troops, d.Troop)
		}
	}
	return troops
}
