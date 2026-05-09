package util

import "git.noncepad.com/pkg/solpipe-util/logger"

const SectionSolpipeHook logger.Section = 1

var LoggerBrainSimple = logger.Standard{Category: logger.CategoryTunnel, Section: SectionSolpipeHook, Code: 1}
