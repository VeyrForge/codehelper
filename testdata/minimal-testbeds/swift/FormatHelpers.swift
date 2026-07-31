import Foundation

struct FormatHelpers: NameFormatter {
    func format(_ s: String) -> String {
        return s.trimmingCharacters(in: .whitespaces)
    }

    func apply(_ s: String) -> String {
        return format(s)
    }
}
