/** An access point's detail, wizard and findings in Vietnamese. A total translation of `../nas.ts`. */
import type { Translation } from "../catalogue.js";
import type { nas } from "../nas.js";

export const vi: Translation<typeof nas> = {
  "nas.reach.unavailable": "Hiện không kết nối được thiết bị này.",
  "nas.detail.probe": "Kiểm tra lại",
  "nas.notice.unreachable.title": "Không kết nối được thiết bị",
  "nas.notice.unreachable.body":
    "Không cấp hay thu hồi được phiên ở đây cho tới khi thiết bị trả lời lại. Các phiên đang chạy vẫn tiếp tục.",
  "nas.notice.credentialsExpired.title": "Khoá API của bộ điều khiển đã hết hạn",
  "nas.notice.credentialsExpired.body": "Cấp khoá mới trên bộ điều khiển, rồi cập nhật tại đây. Đây không phải lỗi mạng.",
  "nas.notice.messageAuthenticator.title": "Thiết bị từ chối Message-Authenticator",
  "nas.notice.messageAuthenticator.body":
    "Thiết bị này có nguy cơ bị tấn công BlastRADIUS (CVE-2024-3596): kẻ tấn công trên đường truyền có thể giả mạo một Access-Accept từ một Access-Reject thật. Foyer vẫn xuất cấu hình an toàn và không bao giờ lặng lẽ hạ mức bảo vệ, nên cảnh báo này còn cho tới khi thiết bị được thay hoặc cập nhật firmware.",
  "nas.notice.noGarden.title": "Trình điều khiển không hỗ trợ vùng truy cập tự do",
  "nas.notice.noGarden.body":
    "Trạng thái trước xác thực trên thiết bị này không bị giới hạn vào đích nào. Bản thân cổng đăng nhập vẫn hoạt động bình thường.",
  "nas.notice.noAccounting.title": "Chưa gắn cấu hình kế toán",
  "nas.notice.noAccounting.body":
    "Thiết bị này không gửi bản ghi phiên nào. Với đường cấp quyền qua API của bộ điều khiển thì không có Access-Accept, nên Foyer không có kênh nào để yêu cầu cập nhật định kỳ.",
  "nas.newSecret.title": "Khoá bí mật dùng chung mới",
  "nas.newSecret.never":
    "Không thao tác nào trả nó về, trong bảng điều khiển này hay trong API, và Foyer không có giá trị mặc định để dùng thay.",
  "nas.newSecret.continue": "Xong — tôi đã lưu khoá này",
  "nas.device.section": "Thiết bị",
  "nas.device.model": "Mẫu",
  "nas.device.firmware": "Firmware",
  "nas.device.address": "Địa chỉ",
  "nas.device.identifier": "NAS-Identifier",
  "nas.device.coaPort": "Cổng CoA",
  "nas.device.coaPortAbsent": "Không dùng CoA.",
  "nas.device.coaPortDefault": "Chưa đặt — Foyer gửi tới 3799. Cisco thường dùng 1700.",
  "nas.device.coaVendor": "Hãng CoA",
  "nas.device.coaVendorUnset": "Chưa chọn",
  "nas.device.coaVendorUnsetWhy":
    "Foyer từ chối điều khiển thiết bị này thay vì đoán kiểu giao tiếp. Chọn một hãng nghĩa là đăng ký lại thiết bị.",
  "nas.secret.section": "Khoá bí mật RADIUS dùng chung",
  "nas.secret.set": "Đã đặt",
  "nas.secret.unset": "Chưa đặt",
  "nas.secret.lastUsed": "Lần dùng cuối",
  "nas.secret.neverUsed": "Chưa dùng lần nào.",
  "nas.secret.note": "Khoá bí mật được tạo khi đăng ký và hiển thị một lần. API không bao giờ trả về khoá đã lưu.",
  "nas.secret.rotate": "Tạo khoá bí mật mới",
  "nas.capabilities.section": "Khả năng của thiết bị",
  "nas.capabilities.emphasis": "Những gì thiết bị này không làm được sẽ không xuất hiện ở các màn hình khác.",
  "nas.capabilities.note":
    "Các khả năng này được đo, không phải tự khai. Foyer không bao giờ hiển thị một trường mà nó sẽ lặng lẽ bỏ qua.",
  "nas.capabilities.bandwidth": "Giới hạn băng thông",
  "nas.capabilities.hardQuota": "Tự áp dụng hạn mức",
  "nas.capabilities.hardQuotaNo": "— hạn mức ở đây được lấy mẫu, và có thể bị vượt",
  "nas.capabilities.dynamicVlan": "VLAN động",
  "nas.capabilities.liveReauthorize": "Đổi giới hạn giữa phiên",
  "nas.capabilities.liveReauthorizeNo": "— thay đổi có hiệu lực khi khách kết nối lại",
  "nas.capabilities.rotateSecret": "Đẩy khoá bí mật dùng chung mới",
  "nas.evidence.section": "Điều này được biết bằng cách nào",
  "nas.evidence.standing.measured": "Đã đo",
  "nas.evidence.standing.documented": "Có tài liệu",
  "nas.evidence.standing.unverified": "Chưa kiểm chứng",
  "nas.evidence.lead":
    "Hai câu hỏi riêng, và có câu trả lời khác nhau. Những gì Foyer sẽ gửi đi phần lớn đã có tài liệu; còn thiết bị này có tuân theo hay không thì chưa được quan sát ở đây.",
  "nas.evidence.capabilities": "Trình điều khiển này nói nó làm được gì",
  "nas.evidence.capabilitiesWhy":
    "Một khả năng quyết định một nút điều khiển có xuất hiện trên các màn hình này hay không. Những gì ghi Chưa kiểm chứng là được tin chứ chưa quan sát — ghi chú cho biết cần gì để xác định.",
  "nas.evidence.attributes": "Foyer sẽ gửi gì",
  "nas.evidence.attributesWhy":
    "Các thuộc tính RADIUS mà cấu hình hãng của thiết bị này có thể xuất. Các thuộc tính chuẩn được định nghĩa trong RFC. Các thuộc tính chuyển hướng không phải chuẩn — chúng được đọc từ bảng của hãng, và chưa thiết bị nào của hãng này trả lời một thuộc tính như vậy ở đây.",
  "nas.evidence.vendorNotChosen":
    "Chưa chọn hãng cho thiết bị này, nên Foyer không nói được nó sẽ gửi gì. Cisco và Aruba nhận diện phiên bằng các thuộc tính khác nhau, và đoán sai còn tệ hơn từ chối: thiết bị báo thành công mà không ngắt kết nối ai.",
  "nas.action.bandwidth": "Đặt giới hạn tốc độ cho một lô phiếu",
  "nas.action.garden": "Sửa vùng truy cập tự do",
  "nas.probe.section": "Lần kiểm tra gần nhất",
  "nas.probe.pass": "Đạt",
  "nas.probe.fail": "Không đạt",
  "nas.probe.skipped": "Bỏ qua",
  "nas.rotate.title": "Tạo khoá bí mật dùng chung mới",
  "nas.rotate.verb": "Tạo khoá bí mật mới",
  "nas.rotate.consequence": "Việc này ghi lại cấu hình FreeRADIUS và khởi động lại tiến trình.",
  "nas.rotate.consequenceEmphasis":
    "Mọi phiên trao đổi EAP đang diễn ra bị huỷ và bộ nhớ đệm phiên TLS bị xoá, nên mọi máy tính 802.1X tại địa điểm này phải xác thực lại.",
  "nas.rotate.recoveryPush": "Trình điều khiển này đẩy được khoá bí mật mới tới thiết bị, nên không ai phải nhập tay.",
  "nas.rotate.recoveryManualEmphasis": "Trình điều khiển này không đẩy được khoá bí mật mới.",
  "nas.rotate.recoveryManual":
    "Thiết bị ngừng xác thực cho tới khi có người nhập khoá bí mật mới trên nó. Hãy chắc bạn truy cập được thiết bị trước khi tiếp tục.",
  "nas.rotate.blockedTitle": "Không thể đổi khoá bí mật của thiết bị này",
  "nas.rotate.blockedBecause": (reach: string) =>
    `${reach} Trình điều khiển cũng không ghi được khoá mới lên thiết bị, nên không ai được biết là có thể mang nó tới đó. Đổi khoá sẽ khiến thiết bị rời khỏi mạng mà không có cách nào đưa giá trị mới lên lại — máy chủ sẽ từ chối, và không có gì thay đổi cho tới khi một trong hai điều đó được sửa.`,
  "nas.rotate.blockedProbe": "Kiểm tra thiết bị trước",
  "nas.rotate.blockedOr": "hoặc thông tin đăng nhập của bộ điều khiển có thể đã sai",

  "nas.rotate.revealedPush":
    "Foyer đã ghi giá trị này lên chính thiết bị, nên không có gì để nhập tay. Giữ lại bản này: đó là cách duy nhất để biết hai bên còn khớp không.",
  "nas.rotate.revealedManual":
    "Foyer không ghi được giá trị này lên thiết bị. Thiết bị sẽ không xác thực được gì cho tới khi có người nhập giá trị này vào đó — hãy chắc chắn có thể tới được nó trước khi tiếp tục.",
  "nas.delete.title": "Xoá điểm truy cập",
  "nas.delete.verb": "Xoá điểm truy cập",
  "nas.delete.consequence": "Thiết bị này bị gỡ khỏi Foyer. Khoá bí mật dùng chung của nó bị huỷ và không khôi phục được.",
  "nas.delete.consequenceEmphasis":
    "Xoá không phải là thu hồi. FreeRADIUS giữ thiết bị này trong bộ nhớ, nên máy chủ đang chạy vẫn tiếp tục xác thực thiết bị sau khi dòng đã bị xoá.",
  "nas.delete.recoveryEmphasis": "Khởi động lại FreeRADIUS nếu cần dừng ngay.",
  "nas.delete.recovery":
    "Đó là cách duy nhất có thời hạn rõ ràng. Nếu để nguyên, một thiết bị mà FreeRADIUS tự phát hiện sẽ bị bỏ khi định nghĩa lưu tạm của nó hết hạn; bao lâu thì tuỳ một cài đặt trên máy chủ mà Foyer không đọc, và chưa ai đo thời gian hết hạn. Thiết bị được máy chủ nạp lúc khởi động thì hoàn toàn không hết hạn và được giữ cho tới khi máy chủ khởi động lại.",
  "nas.delete.records":
    "Các bản ghi phiên hiện có vẫn còn — dữ liệu kế toán thuộc về FreeRADIUS và không bị xoá theo thiết bị. Dùng lại thiết bị nghĩa là đăng ký lại với khoá bí mật mới.",
  "nas.create.title": "Thêm điểm truy cập",
  "nas.create.name": "Tên",
  "nas.create.nameHint": "Tên nhân viên sẽ thấy, ví dụ “Tầng 2 — cạnh cửa sổ”.",
  "nas.create.nameRequired": "Đặt tên cho thiết bị trước khi đăng ký.",
  "nas.create.viaController":
    "Điểm truy cập UniFi, Omada và Ruckus được đọc từ bộ điều khiển của chúng thay vì thêm ở đây. Hãy thêm bộ điều khiển và chạy dò tìm.",
  "nas.create.goToControllers": "Tới bộ điều khiển",
  "nas.create.driver": "Loại thiết bị",
  "nas.create.coaVendor": "Hãng CoA",
  "nas.create.coaVendorUnset": "Chưa chọn",
  "nas.create.coaVendorHint":
    "Cisco và Aruba nhận diện phiên bằng các thuộc tính khác nhau, nên một yêu cầu sai kiểu không khớp gì — và nhiều thiết bị vẫn xác nhận nó, báo một lần thu hồi mà không thu hồi ai.",
  "nas.create.ip": "Địa chỉ IP",
  "nas.create.ipRequired": "Bắt buộc — RADIUS nhận diện thiết bị qua địa chỉ của nó.",
  "nas.create.ipHint":
    "Một địa chỉ hoặc một dải, ví dụ 192.0.2.10 hoặc 192.0.2.0/24. Khả năng được kiểm tra sau khi đăng ký; trước đó Foyer coi như thiết bị không làm được gì, chứ không phải ngược lại.",
  "nas.create.submit": "Đăng ký thiết bị",
  "nas.create.secretTitle": "Khoá bí mật RADIUS dùng chung",
  "nas.create.secretBody":
    "Nhập khoá bí mật này trên thiết bị. Nó chỉ hiển thị một lần — không API nào trả về khoá đã lưu, và Foyer không có khoá mặc định.",
  "nas.create.secretDone": "Tôi đã sao chép",
  "nas.create.failed.title": "Thiết bị chưa được đăng ký",
  "nas.wizard.title": "Đăng ký điểm truy cập",
  "nas.wizard.step": (index: number, total: number) => `Bước ${index} / ${total}`,
  "nas.wizard.step.details": "Thiết bị",
  "nas.wizard.step.secret": "Khoá bí mật dùng chung",
  "nas.wizard.step.probe": "Thiết bị làm được gì",
  "nas.wizard.secret.heading": "Giá trị này chỉ hiển thị một lần",
  "nas.wizard.secret.body":
    "Nhập nó trên thiết bị. Không thao tác nào trả nó về, trong bảng điều khiển này hay trong API, và Foyer không có giá trị mặc định để dùng thay.",
  "nas.wizard.secret.copy": "Sao chép khoá bí mật",
  "nas.wizard.secret.copied": "Đã sao chép",
  "nas.wizard.secret.copyRefused": "Trình duyệt từ chối bộ nhớ tạm. Chọn giá trị ở trên và sao chép bằng tay.",
  "nas.wizard.secret.ack": "Tôi đã lưu khoá bí mật này",
  "nas.wizard.secret.ackWhy":
    "Rời đi mà không có nó sẽ để lại một thiết bị đã đăng ký nhưng không xác thực được. Cách duy nhất là tạo khoá bí mật khác, mà với một số trình điều khiển nghĩa là phải mang nó tới tận thiết bị.",
  "nas.wizard.secret.ackRequired": "Xác nhận bạn đã lưu khoá bí mật. Nó không hiển thị lại.",
  "nas.wizard.secret.continue": "Tiếp tục — kiểm tra thiết bị",
  "nas.wizard.probe.busy": "Đang hỏi thiết bị nó hỗ trợ những gì",
  "nas.wizard.probe.busyNote":
    "Khả năng được đo, không bao giờ được giả định. Cho tới khi có câu trả lời, Foyer coi như thiết bị không làm được gì.",
  "nas.wizard.probe.retry": "Kiểm tra lại",
  "nas.wizard.probe.failed.title": "Lần kiểm tra không chạy",
  "nas.wizard.probe.failed.body":
    "Foyer không hoàn tất được lần kiểm tra, nên chưa đo được gì. Thiết bị vẫn đăng ký với khoá bí mật đã cấp.",
  "nas.wizard.probe.stands": "Thiết bị đã đăng ký và khoá bí mật đã được lưu. Kiểm tra lại sau này không thay đổi điều nào.",
  "nas.wizard.probe.done": "Mở điểm truy cập này",
  "nas.wizard.verdict.clean": "Mọi thứ Foyer kiểm tra đều được thiết bị này hỗ trợ.",
  "nas.wizard.verdict.findings": (n: number) =>
    n === 1 ? "Một điều cần biết trước khi dựa vào thiết bị này." : `${n} điều cần biết trước khi dựa vào thiết bị này.`,
  "nas.wizard.verdict.unmeasured":
    "Chưa có gì bên dưới được đo. Foyer coi thiết bị chưa kiểm tra là không làm được gì thay vì giả định ngược lại.",
  "nas.finding.reachability.title": "Liên lạc với thiết bị",
  "nas.finding.reachability.ok": "Foyer đã liên lạc được với thiết bị và đọc được những gì sau đây từ nó.",
  "nas.finding.reachability.unreachable":
    "Foyer không liên lạc được với thiết bị, nên chưa đo được gì khác ở đây. Việc đăng ký vẫn giữ và khoá bí mật đã được lưu — đây là vấn đề mạng hoặc địa chỉ, không phải vấn đề đăng ký.",
  "nas.finding.reachability.credentialsExpired":
    "Bộ điều khiển có trả lời và từ chối khoá API. Cấp khoá mới trên bộ điều khiển và cập nhật tại đây; mạng không có vấn đề.",
  "nas.finding.reachability.unknown": "Chưa kiểm tra.",
  "nas.finding.messageAuthenticator.title": "Message-Authenticator",
  "nas.finding.messageAuthenticator.enforced":
    "Thiết bị chấp nhận thuộc tính Foyer yêu cầu, nên không giả mạo được một Access-Accept từ một lần từ chối thật.",
  "nas.finding.messageAuthenticator.refused":
    "Thiết bị này có nguy cơ bị tấn công BlastRADIUS (CVE-2024-3596). Foyer vẫn xuất cấu hình an toàn và không bao giờ lặng lẽ hạ mức bảo vệ, nên điều này còn cho tới khi firmware được cập nhật hoặc thiết bị được thay.",
  "nas.finding.accounting.title": "Kế toán phiên",
  "nas.finding.accounting.configured": "Thiết bị này sẽ gửi bản ghi phiên.",
  "nas.finding.accounting.missing":
    "Thiết bị này sẽ không gửi bản ghi phiên nào, nên mọi số liệu phiên của nó để trống. Với đường cấp quyền qua API của bộ điều khiển thì không có Access-Accept để yêu cầu chúng — hãy gắn cấu hình kế toán trên bộ điều khiển.",
  "nas.finding.walledGarden.title": "Vùng truy cập tự do",
  "nas.finding.walledGarden.fqdnSni": "Lưu lượng trước xác thực bị giới hạn theo tên, nên một mục cho phép nghĩa là một đích.",
  "nas.finding.walledGarden.ipRange":
    "Ở đây chỉ cho phép được theo dải IP. Một dải CDN phục vụ nhiều khách hàng không liên quan, nên một mục cho phép rộng hơn hẳn vẻ ngoài của nó.",
  "nas.finding.walledGarden.none":
    "Lưu lượng trước xác thực trên thiết bị này không bị giới hạn vào đích nào. Cổng đăng nhập vẫn hoạt động; trạng thái chưa xác thực không có giới hạn.",
  "nas.finding.hardQuota.title": "Áp dụng hạn mức",
  "nas.finding.hardQuota.yes": "Thiết bị tự áp dụng dung lượng được cấp, nên giới hạn dừng khách đúng ở giới hạn.",
  "nas.finding.hardQuota.no":
    "Ở đây hạn mức chỉ là biện pháp dự phòng dựa trên lấy mẫu, không phải mức trần. Bộ đếm chỉ thay đổi khi có cập nhật định kỳ, mà phần lớn hãng đặt sàn năm phút, nên khách dùng vượt dung lượng trước khi Foyer thấy được.",
  "nas.finding.bandwidthLimits.title": "Giới hạn băng thông",
  "nas.finding.bandwidthLimits.yes": "Có thể áp dụng giới hạn tốc độ theo gói.",
  "nas.finding.bandwidthLimits.no": "Các trường giới hạn tốc độ không xuất hiện cho thiết bị này ở bất kỳ đâu trong bảng điều khiển.",
  "nas.finding.dynamicVlan.title": "VLAN động",
  "nas.finding.dynamicVlan.yes": "Có thể gán VLAN theo từng danh tính.",
  "nas.finding.dynamicVlan.no": "Việc gán VLAN không xuất hiện cho thiết bị này ở bất kỳ đâu trong bảng điều khiển.",
  "nas.finding.liveReauthorize.title": "Thay đổi phiên đang chạy",
  "nas.finding.liveReauthorize.yes": "Có thể đẩy giới hạn đã đổi tới các phiên đang chạy.",
  "nas.finding.liveReauthorize.no":
    "Giới hạn đã đổi có hiệu lực khi khách kết nối lại. Thiết bị này không có chức năng áp dụng ngay.",
  "nas.finding.rotateSecret.title": "Đẩy khoá bí mật dùng chung mới",
  "nas.finding.rotateSecret.yes": "Foyer ghi được khoá bí mật mới lên thiết bị, nên đổi khoá không cần ai đứng cạnh nó.",
  "nas.finding.rotateSecret.no":
    "Foyer tạo được khoá bí mật mới nhưng không ghi được lên thiết bị. Đổi khoá nghĩa là nhập tay ở đó, nên hãy lên lịch đổi khoá vào lúc có người tới được thiết bị.",
  "nas.finding.unmeasured": "Chưa đo — Foyer không đọc được điều này từ thiết bị.",
  "nas.finding.unverified":
    "Được tin chứ chưa đo — không tài liệu hãng hay phép đo trong phòng thử nghiệm nào xác định giá trị này, nên hãy coi nó là chưa biết chứ không phải một bảo đảm.",
  "nas.finding.verdict.supported": "Hỗ trợ",
  "nas.finding.verdict.unsupported": "Không hỗ trợ",
  "nas.finding.verdict.notMeasured": "Chưa đo",
  "nas.finding.verdict.unverified": "Chưa kiểm chứng",
  "nas.finding.verdict.quotaDevice": "Do thiết bị",
  "nas.finding.verdict.quotaSampled": "Lấy mẫu",
  "nas.probeWarning.accessPointUnconfirmed":
    "Điểm truy cập mà lệnh chuyển hướng của khách khai báo không được đối chiếu với bộ điều khiển này — bản ghi thiết bị của nó không có trường nào để so sánh. MAC, site và trạng thái đã được cấp quyền thì có được kiểm tra.",
  "nas.probeWarning.versionBelowFloor": (version: string, minimum: string) =>
    `Đang chạy ${version}; trình điều khiển này cần ${minimum} trở lên.`,
  "nas.probeWarning.capabilitiesUnverified": (n: number) =>
    `${n} khả năng bên dưới được tin chứ chưa được đo.`,
  "nas.probeWarning.unknown": "Một tình trạng mà bảng điều khiển này không nhận ra.",
  "nas.probeWarning.foundTitle": "Phát hiện trên thiết bị này",
  "nas.probeWarning.standingTitle": "Luôn đúng với trình điều khiển này",
  "nas.probeWarning.standingLead":
    "Không phải lỗi, và không riêng thiết bị này — chúng xuất phát từ những gì API của hãng cung cấp, và mọi lần kiểm tra trình điều khiển này đều báo chúng. Chúng có ở đây vì một màn hình bỏ chúng đi sẽ nói với bạn rằng đã có một phép kiểm tra diễn ra.",
};
