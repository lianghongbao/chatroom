package biz

import (
	"unicode"
	"unicode/utf8"
)

// 表情消息的正文约束。
//
// 表情是「一个图形」，不是一个字符串 —— 消息列表里「类型是表情、
// 内容是一整段话」的记录没法渲染。所以表情消息的正文必须收敛到
// 「一个图形」。
const (
	// maxEmojiRunes 表情正文允许的最大码位数。
	//
	// 单码位表情（😀）是 1；带变体选择符的（❤️）是 2；带 ZWJ 的家庭
	// 组合表情（👨‍👩‍👧‍👦）是 7。留 16 的余量覆盖长的 ZWJ 序列
	// （👩‍❤️‍💋‍👨 是 8）和旗帜序列。
	maxEmojiRunes = 16
)

// 表情序列里的修饰码位 —— 它们不构成独立的字形，
// 是依附在主字形上的「变体」。
const (
	// emojiZWJ 零宽连接符（U+200D）—— 把多个码位连成一个字形。
	emojiZWJ = '‍'
	// emojiVariationSelector 变体选择符（U+FE0F）—— 让符号按 emoji 样式渲染。
	emojiVariationSelector = '️'
	// emojiKeycap 组合键帽（U+20E3）—— "1️⃣" 这类数字表情的收尾码位。
	emojiKeycap = '⃣'
	// emojiTagStart 标签起始（U+E0020）—— 国旗序列（🏴󠁧󠁢󠁥󠁮󠁧󠁿）的组成部分。
	emojiTagStart = '\U000E0020'
	// emojiTagEnd 标签结束（U+E007F）。
	emojiTagEnd = '\U000E007F'
)

// isEmojiContent 判断正文是不是一个表情。
// content 表情正文
//
// 放行的形状：
//   - 单个 Unicode 码位（😀）
//   - 符号 + 变体选择符（❤️ = U+2764 U+FE0F）
//   - 修饰符（肤色 👍🏻 = U+1F44D U+1F3FB）
//   - ZWJ 组合序列（👨‍👩‍👧）
//   - 国旗序列（🇨🇳 / 🏴󠁧󠁢󠁥󠁮󠁧󠁿）
//   - 键帽序列（1️⃣ = U+0031 U+FE0F U+20E3）
//
// 拒绝的：普通文字、纯空白、多个不相关的表情拼在一起、长度超限。
//
// 判断分两步：
//  1. 每个码位都要么是 emoji 区段里的字形，要么是已知的修饰码位。
//     落在区段外 → 不是表情，拒。
//  2. 字形数要恰好是 1 —— 多个表情拼一起不是一个表情。
//     ZWJ / 肤色 / 变体选择符这些修饰码位不另计字形。
//
// 刻意不校验「是不是真 emoji」：完整判断要维护 Unicode emoji 码表
// （几千个码位，还要跟版本走），收益不大 —— 客户端是自己写的，
// 表情面板里选什么就是什么。真正要防的是「拿表情类型夹带任意文本」，
// 区段判断加字形数两条就够。
func isEmojiContent(content string) bool {
	if content == "" {
		return false
	}
	// 码位数量超限 —— 多个表情拼一起就不是「一个表情」了。
	if utf8.RuneCountInString(content) > maxEmojiRunes {
		return false
	}
	// 表情里不该有空白 —— 有的话基本都是文本夹带。
	for _, r := range content {
		if unicode.IsSpace(r) {
			return false
		}
	}

	// 第一步：逐个码位分类。
	runes := []rune(content)
	glyphs := 0
	// pending 表示「上一个码位是连接符」—— 下一个字形要跟上一个并进
	// 同一个字形，不另起一个。
	pending := false
	// regionalPending 表示「上一个码位是区域指示符」—— 下一个区域指示符
	// 要跟上一个配成一个国旗。
	regionalPending := false
	// keycap 表示「后面跟着键帽收尾码位」—— 当前字形是键帽序列的底座。
	// sawKeycap 表示整串里出现过键帽收尾码位。
	sawKeycap := false

	for i, r := range runes {
		switch {
		case r == emojiZWJ:
			// 连接符：把前后两个码位连成一个字形。
			pending = true
			continue
		case r == emojiVariationSelector:
			// 修饰码位：依附在主字形上，不另计。
			continue
		case r == emojiKeycap:
			// 键帽收尾码位 —— 依附在底座字符上。
			sawKeycap = true
			continue
		case r >= emojiTagStart && r <= emojiTagEnd:
			// 国旗标签区段：不是字形，是国旗序列的组成部分。
			continue
		}

		// 肤色修饰符（U+1F3FB..U+1F3FF）—— 依附在主字形上。
		if isSkinToneModifier(r) {
			continue
		}

		// 键帽序列的底座（"1" / "#" / "*"）—— 只有后面跟着 U+20E3
		// 才是表情。孤零零一个 "1" 是普通字符，不是表情。
		if isKeycapBase(r) {
			if !hasKeycapAfter(runes, i) {
				return false
			}
			glyphs++
			continue
		}

		// 区段外的码位 —— 不是 emoji。
		if !isEmojiRune(r) {
			return false
		}

		if pending {
			// 跟在连接符后面 —— 并进上一个字形。
			pending = false
			continue
		}

		// 区域指示符（国旗）成对出现 —— 「🇨🇳」是两个码位、一个字形。
		if isRegionalIndicator(r) {
			if regionalPending {
				// 第二个 —— 并进前一个，成一个国旗。
				regionalPending = false
				continue
			}
			regionalPending = true
			glyphs++
			continue
		}

		glyphs++
	}

	// 出现了键帽收尾码位但底座不对 —— 不是合法的键帽序列。
	if sawKeycap && glyphs == 0 {
		return false
	}

	// 第二步：恰好一个字形。
	return glyphs == 1
}

// isKeycapBase 判断码位是不是键帽序列的底座字符。
//
// 底座是 ASCII 数字、"#" 或 "*" —— 它们只有在后面跟着组合键帽
// （U+20E3）时才是表情。
func isKeycapBase(r rune) bool {
	return (r >= '0' && r <= '9') || r == '#' || r == '*'
}

// hasKeycapAfter 判断 runes 里从 idx 往后有没有组合键帽收尾码位。
// runes 码位序列
// idx 底座字符的位置
//
// 底座和键帽之间可以隔一个变体选择符 —— "1️⃣" 是
// U+0031 U+FE0F U+20E3。
func hasKeycapAfter(runes []rune, idx int) bool {
	for j := idx + 1; j < len(runes); j++ {
		switch runes[j] {
		case emojiVariationSelector:
			continue
		case emojiKeycap:
			return true
		default:
			return false
		}
	}
	return false
}

// isSkinToneModifier 判断码位是不是肤色修饰符（U+1F3FB..U+1F3FF）。
//
// 肤色是「同一个小人换个颜色」，不是另一个人 —— 不该算成两个字形。
func isSkinToneModifier(r rune) bool {
	return r >= 0x1F3FB && r <= 0x1F3FF
}

// isRegionalIndicator 判断码位是不是区域指示符（U+1F1E6..U+1F1FF）——
// 国旗序列的组成部分，两个合成一个国旗字形。
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// isEmojiRune 判断一个码位是不是 emoji 字形。
// r 待判断的码位
//
// 覆盖的区段（Unicode 的 emoji 相关块）：
//   - U+1F300..U+1FAFF  各类图形符号、表情、补充符号（含 🀄..🫿）
//   - U+1F000..U+1F02F  麻将牌、domino 牌
//   - U+1F1E6..U+1F1FF  区域指示符（国旗的两半）
//   - U+2600..U+27BF    杂项符号 + 装饰符号（☀..➿，含 ⚠ ✅ ❌ ❤ 等）
//   - U+2190..U+21FF    箭头（←..⇿）
//   - U+2B00..U+2BFF    其它符号与箭头（⭐ ⬛ 等）
//   - U+2000..U+2BFF    补上 U+203C 双感叹号、U+2049 感叹问号、
//     U+2122 商标、U+2139 信息、U+2194..U+21AA、U+231A..U+23FA
//     （⌚ ⌛ ⌨ ⏰ ⏳ 等带 emoji 呈现的符号）
//   - U+25A0..U+25FF    几何图形（▶ ◀ ◼ 等）
//   - U+2B00..U+2BFF    同上
//   - U+3297..U+3299    ㊗ ㊙
//   - U+3030, U+303D    波浪号 / 部件号
//   - U+00A9, U+00AE    版权 / 注册商标
//   - U+2000..U+2BFF 里的数字与符号（键帽序列的底座 0-9 # * 单独处理）
//
// 键帽序列的底座（U+0030..U+0039、U+0023、U+002A）是 ASCII ——
// 它们只在后面跟着 U+20E3 时才是表情，所以单独放行。
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF:
		return true
	case r >= 0x1F000 && r <= 0x1F02F:
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return true
	// 带方框的字母数字（🆎 🈚 🅰 …）—— 其中一部分有 emoji 呈现。
	case r >= 0x1F100 && r <= 0x1F1FF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r >= 0x2190 && r <= 0x21FF:
		return true
	case r >= 0x2B00 && r <= 0x2BFF:
		return true
	case r >= 0x25A0 && r <= 0x25FF:
		return true
	case r >= 0x2300 && r <= 0x23FF:
		return true
	case r >= 0x3297 && r <= 0x3299:
		return true
	case r >= 0x2000 && r <= 0x2BFF:
		return true
	case r == 0x3030 || r == 0x303D:
		return true
	case r == 0x00A9 || r == 0x00AE:
		return true
	case r == 0x20E3:
		return true
	default:
		return false
	}
}
