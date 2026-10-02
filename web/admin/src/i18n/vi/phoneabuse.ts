/** Phone sign-in's limits and the SMS cap in Vietnamese. A total translation of `../phoneabuse.ts`. */
import type { Translation } from "../catalogue.js";
import type { phoneAbuse } from "../phoneabuse.js";

export const vi: Translation<typeof phoneAbuse> = {
  "abuse.title": "Giới hạn đăng nhập bằng điện thoại",
  "abuse.lede":
    "Những gì ngăn một người, một điện thoại hay một địa chỉ tiêu hết tin nhắn của địa điểm. Mỗi giới hạn có thể tăng hoặc giảm trong một khoảng cố định; không giới hạn nào tắt được.",
  "abuse.defaults": "Đây là các giá trị mặc định của Foyer.",
  "abuse.perNumberPerHour": "Số mã mỗi số điện thoại, mỗi giờ",
  "abuse.perNumberPerDay": "Số mã mỗi số điện thoại, mỗi ngày",
  "abuse.perDevicePerHour": "Số mã mỗi thiết bị, mỗi giờ",
  "abuse.perAddressPerHour": "Số mã mỗi địa chỉ Internet, mỗi giờ",
  "abuse.perAddressHint":
    "Tăng giá trị này nếu mọi khách vào cổng đăng nhập qua một địa chỉ dùng chung — NAT, hoặc cổng đăng nhập đặt trên đám mây sau một proxy — nếu không, hai mươi khách một giờ sẽ dùng hết.",
  "abuse.codeMinutes": "Số phút một mã có hiệu lực",
  "abuse.maxAttempts": "Số lần thử mỗi mã",
  "abuse.range": (min: number, max: number) => `Từ ${min} đến ${max}.`,
  "abuse.prefixes": "Các đầu số di động được nhận mã",
  "abuse.prefixesHint": "Chỉ số di động Việt Nam mới nhận được mã. Bỏ đánh dấu một đầu số để ngừng gửi tới đầu số đó.",
  "abuse.save": "Lưu giới hạn",
  "abuse.saved": "Đã lưu giới hạn",
  "abuse.saveFailed": "Chưa lưu được giới hạn",
  "cap.title": "Số tin nhắn mỗi ngày, toàn hệ thống",
  "cap.lede":
    "Một mức trần thứ hai phía trên giới hạn mỗi ngày của từng địa điểm, tính gộp tất cả địa điểm. Khi đạt mức này, không địa điểm nào gửi mã cho tới ngày mai.",
  "cap.none": "Không có hạn mức toàn hệ thống — giới hạn mỗi ngày của từng địa điểm được áp dụng.",
  "cap.label": "Số tin nhắn mỗi ngày",
  "cap.hint": "Để trống nếu không muốn hạn mức toàn hệ thống.",
  "cap.save": "Lưu hạn mức",
  "cap.saved": "Đã lưu hạn mức",
  "cap.saveFailed": "Chưa lưu được hạn mức",
};
