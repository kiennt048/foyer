package portal

// The guest portal's message catalogue, in Go.
//
// The TypeScript catalogues (`web/portal/src/i18n/en.ts`, `vi.ts`) are the
// authority for what a document says. This file is its checked twin: every key
// the renderer reads must exist in both catalogues with the same value shape,
// and `catalogue_test.go` holds the three together — a key added in one place
// and forgotten in another is a build failure, not a silent fallback.
//
// ── why a twin and not a read ────────────────────────────────────────────────
//
// The renderer runs inside the Go binary on every guest document. Reading the
// TypeScript files at request time would couple serving to the source tree;
// embedding and parsing them would make two parsers argue about comments and
// template literals. A twin with a test is the available shape of "derive the
// stand-in from what it stands in for" across the language seam — the same rule
// the byte budgets already follow.

var catalogueEn = map[string]string{
	"lang.name":                "English",
	"lang.name.vi":             "Tiếng Việt",
	"welcome.title":            "Get online",
	"welcome.network":          "Network",
	"welcome.choose":           "How to sign in",
	"welcome.voucher":          "I have a voucher",
	"welcome.voucherHint":      "Printed on your receipt, or given to you by staff",
	"welcome.account":          "I have an account",
	"welcome.accountHint":      "For staff and long-stay guests",
	"welcome.phone":            "Use my phone number",
	"welcome.phoneHint":        "We text you a 6-digit code",
	"welcome.termsHeading":     "Terms of use",
	"welcome.termsSummary":     "Internet access is provided as is, and we log when you connect and for how long, as required by law.",
	"welcome.agree":            "Agree and get online",
	"welcome.boundary":         "Signing in here is a billing and consent boundary, not a security boundary — this network cannot protect your traffic, so use HTTPS.",
	"voucher.title":            "Enter your code",
	"voucher.label":            "Voucher code",
	"voucher.help":             "12 characters. Capitals, spaces and dashes do not matter.",
	"voucher.submit":           "Connect",
	"voucher.back":             "Back",
	"account.title":            "Sign in",
	"account.username":         "Username",
	"account.password":         "Password",
	"account.submit":           "Connect",
	"account.back":             "Back",
	"phone.title":              "Sign in with your phone",
	"phone.label":              "Mobile number",
	"phone.help":               "A Vietnamese mobile number, for example 0912 345 678. We text you a 6-digit code.",
	"phone.send":               "Send code",
	"phone.back":               "Back",
	"phone.codeTitle":          "Enter your code",
	"phone.sentLede":           "We sent a 6-digit code to the number you entered. It works for {minutes} min.",
	"phone.codeLabel":          "Code",
	"phone.verify":             "Connect",
	"phone.again":              "Use another number or send a new code",
	"connected.title":          "Connected",
	"connected.lede":           "You're online.",
	"connected.plan":           "Plan",
	"connected.duration":       "Time limit",
	"connected.allowance":      "Data",
	"connected.allowanceNote":  "This is your total allowance, not the amount left.",
	"connected.sampledNote":    "Usage is sampled periodically, not measured live. It can be several minutes behind.",
	"connected.unenforcedNote": "The equipment here does not enforce this limit, so treat it as a guide.",
	"connected.continue":       "Continue to",
	"connected.closeHint":      "You can close this window.",
	"connected.expired":        "Your time ran out, so this device is offline now. Ask staff for a new code.",
	"connected.revoked":        "Your access was cancelled, so this device is offline now. Ask staff for a new code.",
	"connected.unknown":        "This device is not signed in. Sign in again to get back online.",
	"connected.notOnNetwork":   "Your code was accepted, but the network did not let this device on. That is a fault here at the venue, not on your device. Show this screen to staff.",
	"connected.signIn":         "Back to sign in",
	"problem.title":            "Not connected",
	"problem.back":             "Try again",
	"error.not-found":          "That code was not found. Check each character and try again.",
	"error.already-used":       "That code has already been used. Ask staff for another one.",
	"error.expired":            "That code has expired. Ask staff for another one.",
	"error.revoked":            "That code has been cancelled. Ask staff for another one.",
	"error.not-present":        "Your device could not be confirmed on this network. Turn Wi-Fi off and on again to start over.",
	"error.ap-unconfirmed":     "Your device is on this network, but the access point it is using could not be confirmed. Reconnect to the Wi-Fi and try again. If that does not help, show this screen to staff.",
	"error.bad-credentials":    "That username or password is not right. Check both and try again.",
	"error.disabled":           "This account is disabled. Ask staff to turn it back on.",
	"error.shape":              "A voucher code is 12 characters. Check yours and try again.",
	"error.generic":            "Something went wrong. Try again.",
	"error.phone-invalid":      "Codes can only be sent to a Vietnamese mobile number. Check the number and try again.",
	"error.code-wrong":         "That code is not right. Check the message and try again.",
	"error.code-expired":       "That code has expired or was already used. Send a new one.",
	"error.code-limit":         "Too many codes were requested. Wait a while and try again, or ask staff.",
	"error.code-unavailable":   "Codes cannot be sent right now. Choose another way to sign in, or ask staff.",
	"error.details-required":   "A detail this venue needs is blank. Fill it in and try again.",
	"error.details-invalid":    "One of your details does not look right. Check it and try again.",
	"details.name":             "Your name",
	"details.phone":            "Phone number",
	"details.email":            "Email address",
	"details.optional":         "optional",
	"notice.consent-withdrawn": "Done. This venue will not send you marketing messages.",
	"connected.withdrawHint":   "Agreed to hear about offers from this venue? You can take that back at any time.",
	"connected.withdraw":       "Stop marketing messages",
	"block.code":               "Code",
	"social.facebook":          "Facebook",
	"social.instagram":         "Instagram",
	"social.tiktok":            "TikTok",
	"social.zalo":              "Zalo",
	"social.youtube":           "YouTube",
	"social.google-review":     "Review us on Google",
	"social.website":           "Website",
	"social.phone":             "Call us",
	"unit.minutes":             "min",
	"unit.hour":                "hour",
	"unit.hours":               "hours",
	"unit.unlimited":           "No limit",
}

var catalogueVi = map[string]string{
	"lang.name":                "Tiếng Việt",
	"lang.name.en":             "English",
	"welcome.title":            "Kết nối Wi-Fi",
	"welcome.network":          "Mạng",
	"welcome.choose":           "Chọn cách đăng nhập",
	"welcome.voucher":          "Tôi có mã phiếu",
	"welcome.voucherHint":      "Mã in trên hoá đơn hoặc do nhân viên cung cấp",
	"welcome.account":          "Tôi có tài khoản",
	"welcome.accountHint":      "Dành cho nhân viên và khách lưu trú dài ngày",
	"welcome.phone":            "Tôi dùng số điện thoại",
	"welcome.phoneHint":        "Nhận mã 6 chữ số qua tin nhắn",
	"welcome.termsHeading":     "Điều khoản sử dụng",
	"welcome.termsSummary":     "Truy cập Internet được cung cấp nguyên trạng, và chúng tôi ghi lại thời điểm cùng thời lượng kết nối của bạn theo quy định pháp luật.",
	"welcome.agree":            "Đồng ý và kết nối",
	"welcome.boundary":         "Đăng nhập ở đây là ranh giới thanh toán và đồng ý, không phải ranh giới bảo mật — mạng này không bảo vệ dữ liệu của bạn, hãy dùng HTTPS.",
	"voucher.title":            "Nhập mã phiếu",
	"voucher.label":            "Mã phiếu",
	"voucher.help":             "12 ký tự. Hoa thường, dấu cách và dấu gạch đều không ảnh hưởng.",
	"voucher.submit":           "Kết nối",
	"voucher.back":             "Quay lại",
	"account.title":            "Đăng nhập",
	"account.username":         "Tên đăng nhập",
	"account.password":         "Mật khẩu",
	"account.submit":           "Kết nối",
	"account.back":             "Quay lại",
	"phone.title":              "Đăng nhập bằng số điện thoại",
	"phone.label":              "Số điện thoại di động",
	"phone.help":               "Số di động Việt Nam, ví dụ 0912 345 678. Chúng tôi sẽ gửi mã 6 chữ số qua tin nhắn.",
	"phone.send":               "Gửi mã",
	"phone.back":               "Quay lại",
	"phone.codeTitle":          "Nhập mã xác thực",
	"phone.sentLede":           "Chúng tôi đã gửi mã 6 chữ số tới số bạn vừa nhập. Mã có hiệu lực trong {minutes} phút.",
	"phone.codeLabel":          "Mã xác thực",
	"phone.verify":             "Kết nối",
	"phone.again":              "Dùng số khác hoặc gửi lại mã",
	"connected.title":          "Đã kết nối",
	"connected.lede":           "Bạn đã có mạng.",
	"connected.plan":           "Gói",
	"connected.duration":       "Giới hạn thời gian",
	"connected.allowance":      "Dung lượng",
	"connected.allowanceNote":  "Đây là dung lượng được cấp, không phải số còn lại.",
	"connected.sampledNote":    "Mức sử dụng được lấy mẫu định kỳ, không theo thời gian thực. Số liệu có thể chậm vài phút.",
	"connected.unenforcedNote": "Thiết bị mạng ở đây không áp mức giới hạn này, nên đây là con số tham khảo.",
	"connected.continue":       "Tiếp tục tới",
	"connected.closeHint":      "Bạn có thể đóng cửa sổ này.",
	"connected.expired":        "Đã hết thời gian, nên thiết bị này không còn mạng. Hãy hỏi nhân viên để lấy mã mới.",
	"connected.revoked":        "Quyền truy cập đã bị hủy, nên thiết bị này không còn mạng. Hãy hỏi nhân viên để lấy mã mới.",
	"connected.unknown":        "Thiết bị này chưa đăng nhập. Hãy đăng nhập lại để có mạng.",
	"connected.notOnNetwork":   "Mã của bạn đã được chấp nhận, nhưng mạng chưa cho thiết bị này vào. Đây là sự cố tại đây, không phải do thiết bị của bạn — hãy cho nhân viên xem màn hình này.",
	"connected.signIn":         "Quay lại đăng nhập",
	"problem.title":            "Chưa kết nối được",
	"problem.back":             "Thử lại",
	"error.not-found":          "Không tìm thấy mã này. Kiểm tra lại từng ký tự rồi nhập lại.",
	"error.already-used":       "Mã này đã được dùng rồi. Hãy hỏi nhân viên để lấy mã khác.",
	"error.expired":            "Mã này đã hết hạn. Hãy hỏi nhân viên để lấy mã khác.",
	"error.revoked":            "Mã này đã bị thu hồi. Hãy hỏi nhân viên để lấy mã khác.",
	"error.not-present":        "Không xác nhận được thiết bị của bạn trên mạng này. Ngắt Wi-Fi rồi kết nối lại để bắt đầu lại.",
	"error.ap-unconfirmed":     "Thiết bị của bạn đang ở trong mạng này, nhưng không xác nhận được điểm truy cập mà thiết bị đang dùng. Ngắt Wi-Fi rồi kết nối lại và thử lại. Nếu vẫn không được, hãy cho nhân viên xem màn hình này.",
	"error.bad-credentials":    "Tên đăng nhập hoặc mật khẩu không đúng. Kiểm tra lại cả hai rồi thử lại.",
	"error.disabled":           "Tài khoản này đang bị khoá. Hãy nhờ nhân viên mở lại.",
	"error.shape":              "Mã phiếu gồm 12 ký tự. Kiểm tra lại mã của bạn rồi thử lại.",
	"error.generic":            "Có lỗi xảy ra. Thử lại lần nữa.",
	"error.phone-invalid":      "Chỉ gửi được mã tới số di động Việt Nam. Kiểm tra lại số rồi thử lại.",
	"error.code-wrong":         "Mã chưa đúng. Kiểm tra tin nhắn rồi nhập lại.",
	"error.code-expired":       "Mã này đã hết hạn hoặc đã được dùng. Hãy gửi mã mới.",
	"error.code-limit":         "Bạn đã yêu cầu quá nhiều mã. Hãy đợi một lúc rồi thử lại, hoặc hỏi nhân viên.",
	"error.code-unavailable":   "Hiện chưa gửi được mã. Hãy chọn cách đăng nhập khác, hoặc hỏi nhân viên.",
	"error.details-required":   "Còn thiếu một thông tin bắt buộc. Hãy điền rồi thử lại.",
	"error.details-invalid":    "Một thông tin của bạn chưa đúng định dạng. Kiểm tra lại rồi thử lại.",
	"details.name":             "Họ và tên",
	"details.phone":            "Số điện thoại",
	"details.email":            "Địa chỉ email",
	"details.optional":         "không bắt buộc",
	"notice.consent-withdrawn": "Đã xong. Địa điểm này sẽ không gửi tin tiếp thị cho bạn nữa.",
	"connected.withdrawHint":   "Đã đồng ý nhận tin ưu đãi từ địa điểm này? Bạn có thể rút lại bất cứ lúc nào.",
	"connected.withdraw":       "Ngừng nhận tin tiếp thị",
	"block.code":               "Mã",
	"social.facebook":          "Facebook",
	"social.instagram":         "Instagram",
	"social.tiktok":            "TikTok",
	"social.zalo":              "Zalo",
	"social.youtube":           "YouTube",
	"social.google-review":     "Đánh giá trên Google",
	"social.website":           "Trang web",
	"social.phone":             "Gọi cho chúng tôi",
	"unit.minutes":             "phút",
	"unit.hour":                "giờ",
	"unit.hours":               "giờ",
	"unit.unlimited":           "Không giới hạn",
}

// renderTranslator returns the lookup for one language. Unknown languages read
// English — the catalogue's default, matching `Language("")`.
func renderTranslator(lang RenderLang) func(string) string {
	cat := catalogueEn
	if lang == "vi" {
		cat = catalogueVi
	}
	otherNameEn := "Tiếng Việt"
	otherNameVi := "English"
	return func(key string) string {
		// The switcher's endonym: each language names the *other* in its own
		// script, so a reader who cannot read the page can still find theirs.
		if key == "lang.name.en" {
			return otherNameVi
		}
		if key == "lang.name.vi" {
			return otherNameEn
		}
		if key == "lang.name" {
			if lang == "vi" {
				return cat["lang.name"]
			}
			return cat["lang.name"]
		}
		return cat[key]
	}
}
