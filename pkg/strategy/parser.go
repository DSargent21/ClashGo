package strategy

type Drop struct {
	X, Y     int
	Troop    string
	Quantity int
	Delay    int
	Slot     int
	DropType string
	Wave     int
}

type AttackStrategy struct {
	Name        string
	Description string
	DeployLine  string
	Sides       int
	DropOrders  []Drop
}
