package wiki

import (
	pbw "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
)

type Preference struct {
	mPreference map[string]prefValue
}
type prefValue struct {
	value     string
	valueType string
}

func (p *Preference) Proto() []*pbw.Preference {
	if len(p.mPreference) == 0 {
		return nil
	}
	list := make([]*pbw.Preference, len(p.mPreference))
	i := 0
	for k, v := range p.mPreference {
		list[i] = &pbw.Preference{
			Name: k, Value: v.value, Type: v.valueType,
		}
		i++
	}
	return list
}
