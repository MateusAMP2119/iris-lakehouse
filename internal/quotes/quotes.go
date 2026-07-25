// Package quotes is the ceremony's motivational quote pool, shared by the
// install/uninstall ceremonies and the ps idle card.
package quotes

// Quote is one attributed entry of the pool.
type Quote struct {
	Author string
	Text   string
}

// Farewell is the built-in pool ceremonies draw from at random.
var Farewell = []Quote{
	{"Heraclitus", "The only constant in life is change."},
	{"Marcus Aurelius", "Everything that happens is either endurable or not. If it is endurable, endure it."},
	{"Lao Tzu", "When you realize nothing is lacking, the whole world belongs to you."},
	{"Nietzsche", "One must still have chaos in oneself to be able to give birth to a dancing star."},
	{"Epictetus", "It's not what happens to you, but how you react to it that matters."},
	{"Socrates (via Plato)", "The unexamined life is not worth living."},
	{"Seneca", "Every new beginning comes from some other beginning's end."},
}
