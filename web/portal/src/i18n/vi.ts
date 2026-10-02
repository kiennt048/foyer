import type { MessageKey } from "./en.js";

/**
 * Vietnamese.
 *
 * Typed as a total map of `MessageKey`, which comes from `en.ts`. English is the
 * source of truth; a key added there and forgotten here is a build error rather
 * than a string that silently renders in the wrong language on a screen the guest
 * sees for eleven seconds.
 *
 * The same copy rules apply: no idiom, no humour, short sentences, every error
 * names a next action. "Vui lòng" is omitted — it lengthens every string without
 * adding meaning.
 */
export const vi: Record<MessageKey, string> = {
  "lang.name": "Tiếng Việt",

  "welcome.title": "Kết nối Wi-Fi",
  "welcome.network": "Mạng",
  "welcome.choose": "Chọn cách đăng nhập",
  "welcome.voucher": "Tôi có mã phiếu",
  "welcome.voucherHint": "Mã in trên hoá đơn hoặc do nhân viên cung cấp",
  "welcome.account": "Tôi có tài khoản",
  "welcome.accountHint": "Dành cho nhân viên và khách lưu trú dài ngày",
  "welcome.phone": "Tôi dùng số điện thoại",
  "welcome.phoneHint": "Nhận mã 6 chữ số qua tin nhắn",
  "welcome.termsHeading": "Điều khoản sử dụng",
  "welcome.termsSummary":
    "Truy cập Internet được cung cấp nguyên trạng, và chúng tôi ghi lại thời điểm cùng thời lượng kết nối của bạn theo quy định pháp luật.",
  "welcome.agree": "Đồng ý và kết nối",
  "welcome.boundary":
    "Đăng nhập ở đây là ranh giới thanh toán và đồng ý, không phải ranh giới bảo mật — mạng này không bảo vệ dữ liệu của bạn, hãy dùng HTTPS.",

  "voucher.title": "Nhập mã phiếu",
  "voucher.label": "Mã phiếu",
  "voucher.help": "12 ký tự. Hoa thường, dấu cách và dấu gạch đều không ảnh hưởng.",
  "voucher.submit": "Kết nối",
  "voucher.back": "Quay lại",

  "account.title": "Đăng nhập",
  "account.username": "Tên đăng nhập",
  "account.password": "Mật khẩu",
  "account.submit": "Kết nối",
  "account.back": "Quay lại",
  "phone.title": "Đăng nhập bằng số điện thoại",
  "phone.label": "Số điện thoại di động",
  "phone.help": "Số di động Việt Nam, ví dụ 0912 345 678. Chúng tôi sẽ gửi mã 6 chữ số qua tin nhắn.",
  "phone.send": "Gửi mã",
  "phone.back": "Quay lại",
  "phone.codeTitle": "Nhập mã xác thực",
  "phone.sentLede": "Chúng tôi đã gửi mã 6 chữ số tới số bạn vừa nhập. Mã có hiệu lực trong {minutes} phút.",
  "phone.codeLabel": "Mã xác thực",
  "phone.verify": "Kết nối",
  "phone.again": "Dùng số khác hoặc gửi lại mã",

  "connected.title": "Đã kết nối",
  "connected.lede": "Bạn đã có mạng.",
  "connected.plan": "Gói",
  "connected.duration": "Giới hạn thời gian",
  "connected.allowance": "Dung lượng",
  "connected.allowanceNote": "Đây là dung lượng được cấp, không phải số còn lại.",
  "connected.sampledNote":
    "Mức sử dụng được lấy mẫu định kỳ, không theo thời gian thực. Số liệu có thể chậm vài phút.",
  "connected.unenforcedNote":
    "Thiết bị mạng ở đây không áp mức giới hạn này, nên đây là con số tham khảo.",
  "connected.continue": "Tiếp tục tới",
  "connected.closeHint": "Bạn có thể đóng cửa sổ này.",

  "connected.expired": "Đã hết thời gian, nên thiết bị này không còn mạng. Hãy hỏi nhân viên để lấy mã mới.",
  "connected.revoked":
    "Quyền truy cập đã bị hủy, nên thiết bị này không còn mạng. Hãy hỏi nhân viên để lấy mã mới.",
  "connected.unknown": "Thiết bị này chưa đăng nhập. Hãy đăng nhập lại để có mạng.",
  "connected.notOnNetwork":
    "Mã của bạn đã được chấp nhận, nhưng mạng chưa cho thiết bị này vào. Đây là sự cố tại đây, không phải do thiết bị của bạn — hãy cho nhân viên xem màn hình này.",
  "connected.signIn": "Quay lại đăng nhập",

  "problem.title": "Chưa kết nối được",
  "problem.back": "Thử lại",

  "error.not-found": "Không tìm thấy mã này. Kiểm tra lại từng ký tự rồi nhập lại.",
  "error.already-used": "Mã này đã được dùng rồi. Hãy hỏi nhân viên để lấy mã khác.",
  "error.expired": "Mã này đã hết hạn. Hãy hỏi nhân viên để lấy mã khác.",
  "error.revoked": "Mã này đã bị thu hồi. Hãy hỏi nhân viên để lấy mã khác.",
  "error.not-present":
    "Không xác nhận được thiết bị của bạn trên mạng này. Ngắt Wi-Fi rồi kết nối lại để bắt đầu lại.",
  "error.ap-unconfirmed":
    "Thiết bị của bạn đang ở trong mạng này, nhưng không xác nhận được điểm truy cập mà thiết bị đang dùng. Ngắt Wi-Fi rồi kết nối lại và thử lại. Nếu vẫn không được, hãy cho nhân viên xem màn hình này.",
  "error.bad-credentials":
    "Tên đăng nhập hoặc mật khẩu không đúng. Kiểm tra lại cả hai rồi thử lại.",
  "error.disabled": "Tài khoản này đang bị khoá. Hãy nhờ nhân viên mở lại.",
  "error.shape": "Mã phiếu gồm 12 ký tự. Kiểm tra lại mã của bạn rồi thử lại.",
  "error.generic": "Có lỗi xảy ra. Thử lại lần nữa.",
  "error.phone-invalid": "Chỉ gửi được mã tới số di động Việt Nam. Kiểm tra lại số rồi thử lại.",
  "error.code-wrong": "Mã chưa đúng. Kiểm tra tin nhắn rồi nhập lại.",
  "error.code-expired": "Mã này đã hết hạn hoặc đã được dùng. Hãy gửi mã mới.",
  "error.code-limit": "Bạn đã yêu cầu quá nhiều mã. Hãy đợi một lúc rồi thử lại, hoặc hỏi nhân viên.",
  "error.code-unavailable": "Hiện chưa gửi được mã. Hãy chọn cách đăng nhập khác, hoặc hỏi nhân viên.",

  "error.details-required": "Còn thiếu một thông tin bắt buộc. Hãy điền rồi thử lại.",
  "error.details-invalid": "Một thông tin của bạn chưa đúng định dạng. Kiểm tra lại rồi thử lại.",
  "details.name": "Họ và tên",
  "details.phone": "Số điện thoại",
  "details.email": "Địa chỉ email",
  "details.optional": "không bắt buộc",
  "notice.consent-withdrawn": "Đã xong. Địa điểm này sẽ không gửi tin tiếp thị cho bạn nữa.",
  "connected.withdrawHint": "Đã đồng ý nhận tin ưu đãi từ địa điểm này? Bạn có thể rút lại bất cứ lúc nào.",
  "connected.withdraw": "Ngừng nhận tin tiếp thị",

  "block.code": "Mã",
  "social.facebook": "Facebook",
  "social.instagram": "Instagram",
  "social.tiktok": "TikTok",
  "social.zalo": "Zalo",
  "social.youtube": "YouTube",
  "social.google-review": "Đánh giá trên Google",
  "social.website": "Trang web",
  "social.phone": "Gọi cho chúng tôi",

  "unit.minutes": "phút",
  "unit.hour": "giờ",
  "unit.hours": "giờ",
  "unit.unlimited": "Không giới hạn",

  // The measurement probe. Same contract as the English block: the labels are prose
  // a person reads, the values beside them are machine tokens the page also POSTs,
  // and a key added in en.ts and forgotten here is a compile error rather than a
  // screen in the wrong language.
  "probe.title": "Thăm dò môi trường",
  "probe.heading": "Thăm dò môi trường thiết bị",
  "probe.lede":
    "Đây không phải trang dành cho khách. Trang này ghi lại những gì trình duyệt này làm, hiện các giá trị, và gửi chúng đến máy đo.",
  "probe.viewport": "Khung nhìn",
  "probe.orientation": "Hướng",
  "probe.ladder": "Nhịp dọc",
  "probe.dark": "Nền tối",
  "probe.insets": "Vùng an toàn",
  "probe.localstorage": "localStorage",
  "probe.sessionstorage": "sessionStorage",
  "probe.persist": "Sau tải lại",
  "probe.cookie": "Cookie",
  "probe.font": "Phông chữ",
  "probe.fontnote": "Hai lần: tam giác = đã tải, chữ = chưa tải.",
  "probe.ua": "User agent",
  "probe.fits": "Hiện đủ",
  "probe.report": "Báo cáo",
  "probe.pending": "chưa đo",
  "probe.sent": "Đã gửi tới máy đo",
  "probe.refused": "Máy đo từ chối báo cáo. Các giá trị phía trên vẫn còn trên màn hình này.",
  "probe.blocked": "Trình duyệt này từ chối gửi báo cáo. Các giá trị phía trên vẫn còn trên màn hình này.",
  "probe.unknown": "không rõ",
};
