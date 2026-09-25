package analysis

// analyzeConcurrency runs the concurrency defect checks.
func (p *Program) analyzeConcurrency() {
	p.checkCopylocks()
	p.checkUnlockPaths()
	p.checkNilChannels()
}
