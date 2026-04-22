package main

// allChecks 注册所有单包检查函数，按分类分组。
//
// 执行顺序：main.go 对每个 CheckPass（一个包）依次调用 allChecks 中的所有函数。
// 每个函数独立运行，返回各自发现的 Finding 列表。函数之间无依赖关系。
//
// 分类对应 main.go 中的 categoryOrder，决定最终报告中的展示顺序。
// 跨包检查（需要聚合多个 CheckPass 信息的检查）不在此注册，
// 而是在 runCrossPackageChecks 中单独调用。
var allChecks = []CheckFunc{
	// 错误处理
	checkErrSwallow,
	checkSentinelWrap,
	checkFatalPanic,
	checkTypeAssert,
	checkErrShadow,
	checkEmptyErrBlock,
	checkDeferErr,
	checkRecoverSilent,
	checkErrorsIsUsage,
	// 日志
	checkLogCoverage,
	checkSensitiveLog,
	checkDuplicateLog,
	checkLogContextFields,
	// 数据库安全
	checkGormSave,
	checkGormNoWhere,
	checkGormNotFound,
	checkGormTOCTOU,
	checkGormMultiTable,
	checkSQLConcat,
	checkGormManualTxRollback,
	checkRowsErr,
	checkGormSelectStar,
	// 并发与资源
	checkBareGoroutine,
	checkRespBodyLeak,
	checkOsOpenLeak,
	checkHTTPTimeout,
	checkCtxCancelLeak,
	checkCtxBackground,
	checkRedisLeak,
	checkWaitGroupAdd,
	checkTimeAfterLoop,
	checkBlockingSelect,
	checkMapConcurrency,
	checkAIProviderTimeout,
	checkSelectCtxDone,
	checkMutexValueCopy,
	checkRespBodyNilGuard,
	checkChannelSendNoSelect,
	checkTimeSleep,
	checkTimeNowUTC,
	checkLockWithoutDefer,
	// Gin 框架安全
	checkGinNoReturn,
	checkGinCtxEscape,
	checkHandlerResponseCoverage,
	checkShouldBindErr,
	checkResponseShape,
	// 安全
	checkHardcodedSecret,
	checkJSONTagMissing,
	checkBindingTagMissing,
	checkEmptyStructTag,
	checkUnexportedReturn,
	// 注释
	checkGodocCoverage,
	checkGodocShallow,
	checkGodocChinese,
	checkSentinelComment,
	checkTypeComment,
	checkLockingComment,
	// checkTestComment 不在此注册，仅在 --include-tests 模式下运行
	// 结构
	checkFileSize,
	checkFuncSize,
	checkFileName,
	checkContextFirst,
	checkDeepNesting,
	checkInterfacePollution,
}

// runCrossPackageChecks 执行需要聚合所有包信息的跨包检查。
//
// 与 allChecks 中的单包检查不同，跨包检查接收完整的 passes 列表，
// 可以跨文件/跨包分析（如检测项目中未被引用的导出符号、必须文件检查、循环依赖）。
// 在 main.go 中于所有单包检查完成后调用一次。
func runCrossPackageChecks(passes []*CheckPass, codeRoot string) []Finding {
	var findings []Finding
	findings = append(findings, checkUnusedExports(passes, codeRoot)...)
	findings = append(findings, checkRequiredFilesCross(passes, codeRoot)...)
	findings = append(findings, checkCircularDep(passes, codeRoot)...)
	findings = append(findings, checkErrorMapCompleteness(passes, codeRoot)...)
	findings = append(findings, checkHandlerRouteCoverage(passes, codeRoot)...)
	return findings
}
