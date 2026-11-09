package settings

// DecisionImpact counts effective held files per version. Upgrade eligibility is the wanted rule: the profile
// permits upgrades and the held file has not reached its quality/format cutoff. No search or write is performed.
type DecisionImpact struct {
	// Files counts the held files evaluated: those of the versions the change can affect. Changed counts the ones
	// decided differently (score, cutoff or upgrade).
	Files          int             `json:"files"`
	Changed        int             `json:"changed"`
	ScoreChanged   int             `json:"score_changed"`
	CutoffChanged  int             `json:"cutoff_changed"`
	UpgradeChanged int             `json:"upgrade_changed"`
	Examples       []ImpactExample `json:"examples"`
}
type ImpactExample struct {
	Title           string `json:"title"`
	Version         string `json:"version"`
	File            string `json:"file"`
	BeforeScore     int    `json:"before_score"`
	AfterScore      int    `json:"after_score"`
	BeforeCutoffMet bool   `json:"before_cutoff_met"`
	AfterCutoffMet  bool   `json:"after_cutoff_met"`
	BeforeUpgrade   bool   `json:"before_upgrade"`
	AfterUpgrade    bool   `json:"after_upgrade"`
}
