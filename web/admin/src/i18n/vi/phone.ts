/** Phone sign-in's provider panel in Vietnamese. A total translation of `../phone.ts`. */
import type { Translation } from "../catalogue.js";
import type { phone } from "../phone.js";

export const vi: Translation<typeof phone> = {
  "phone.lede":
    "Khách nhập số di động Việt Nam và nhận mã 6 chữ số qua SMS hoặc Zalo. Mỗi mã tốn của địa điểm một tin nhắn, nên giới hạn mỗi ngày bên dưới là giới hạn chi tiêu.",
  "phone.unverified.title": "Chưa được kiểm chứng với tài khoản nhà cung cấp thật",
  "phone.unverified.body":
    "Kết nối eSMS được xây dựng từ API eSMS công bố và kiểm thử với một bản giả lập. Sau khi lưu, hãy gửi một mã tới điện thoại của chính bạn trước khi cho khách dùng.",
  "phone.provider": "Nhà cung cấp",
  "phone.provider.esms-sms": "SMS brandname (eSMS)",
  "phone.provider.esms-zalo": "Thông báo Zalo (eSMS)",
  "phone.brandname": "Brandname",
  "phone.brandnameHint":
    "Tên người gửi đã đăng ký với eSMS, đúng như đã đăng ký. Nhà mạng Việt Nam chỉ chuyển SMS brandname từ tên đã đăng ký, qua API HTTP của nhà cung cấp — không qua đường dây điện thoại hay SIP.",
  "phone.zaloOaId": "Mã Zalo Official Account",
  "phone.zaloTemplateId": "Mã mẫu tin Zalo",
  "phone.zaloTemplateHint": "Một mẫu tin đã được duyệt, có một tham số tên là otp.",
  "phone.apiKey": "API key",
  "phone.secretKey": "Secret key",
  "phone.keysHint.unset": "API key và secret key từ tài khoản eSMS của bạn. Được lưu mã hoá và không bao giờ hiển thị lại.",
  "phone.keysHint.set": "Đã lưu. Để trống để giữ khoá hiện tại.",
  "phone.template": "Tin nhắn",
  "phone.templateHint":
    "Viết {code} ở chỗ đặt mã. Với SMS, tin nhắn phải khớp từng ký tự với mẫu brandname eSMS đã duyệt.",
  "phone.dailyLimit": "Số tin nhắn mỗi ngày",
  "phone.dailyLimitHint":
    "Khi đạt tới con số này, đăng nhập bằng điện thoại ngừng gửi cho tới ngày mai và khách được đề nghị dùng cách khác.",
  "phone.sessionMinutes": "Số phút trực tuyến sau khi đăng nhập",
  "phone.sandbox": "Chế độ thử — nhà cung cấp nhận tin nhắn nhưng không gửi đi",
  "phone.sentToday": (sent: number, limit: number) => `Hôm nay đã gửi ${sent} / ${limit} tin nhắn.`,
  "phone.limits": (
    perHour: number,
    perDay: number,
    device: number,
    address: number,
    minutes: number,
    attempts: number,
  ) =>
    `Giới hạn đang áp dụng: mỗi số ${perHour} mã một giờ và ${perDay} mã một ngày, mỗi thiết bị ${device} mã một giờ, mỗi địa chỉ Internet ${address} mã một giờ; mỗi mã dùng được ${minutes} phút và ${attempts} lần thử. Chỉ số di động Việt Nam mới nhận được mã. Thay đổi trong phần Giới hạn đăng nhập bằng điện thoại bên dưới.`,
  "phone.test.title": "Gửi tin nhắn thử",
  "phone.test.label": "Số di động của bạn",
  "phone.test.hint":
    "Gửi tin nhắn ở trên, với một mã ngẫu nhiên không đăng nhập được cho ai, qua nhà cung cấp đã lưu. Đây là tin nhắn thật: nó được tính vào giới hạn hôm nay.",
  "phone.test.send": "Gửi tin nhắn thử",
  "phone.test.failed": "Chưa gửi được tin nhắn thử",
  "phone.test.sent": "Nhà cung cấp đã nhận tin nhắn. Hãy kiểm tra xem nó đã tới chưa.",
  "phone.test.phone-invalid": "Không phải số di động Việt Nam thuộc đầu số đang mở bên dưới.",
  "phone.test.not-configured": "Lưu nhà cung cấp và khoá của nó trước.",
  "phone.test.ceiling-reached": "Đã đạt giới hạn tin nhắn hôm nay, nên không gửi gì.",
  "phone.test.provider-refused": (code: string) =>
    `Nhà cung cấp từ chối tin nhắn (mã kết quả ${code}). Kiểm tra các khoá, brandname, và tin nhắn có khớp mẫu đã đăng ký không.`,
  "phone.test.provider-unreachable": "Nhà cung cấp không trả lời. Thử lại sau một phút.",
  "phone.save": "Lưu đăng nhập bằng điện thoại",
  "phone.saved": "Đã lưu đăng nhập bằng điện thoại",
  "phone.saveFailed": "Chưa lưu được đăng nhập bằng điện thoại",
  "phone.notConfigured": "Chưa thiết lập. Lưu nhà cung cấp ở đây trước khi bật đăng nhập bằng điện thoại ở trên.",
};
