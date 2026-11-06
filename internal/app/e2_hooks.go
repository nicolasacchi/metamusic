package app

// wireE2Hooks is the last E2 wiring step: lane E2-INT-B owns this file (the acceptance seams, optional hooks such as the
// trial and Suite hooks of E2c, and the final registry flip). The freeze leaves it empty and Build calls it after every
// service exists.
func wireE2Hooks(s *Services) {}
