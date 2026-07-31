import Foundation
import MyLib.Helpers

/// Protocol so the bed exercises protocol_declaration + method ParentID.
protocol NameFormatter {
    func format(_ s: String) -> String
}

class Greeter {
    func greet(name: String) -> String {
        return format(name)
    }

    func greetLoud(name: String) -> String {
        return greet(name: name) + "!"
    }
}

extension Greeter {
    func welcome(name: String) -> String {
        return "Hi " + greet(name: name)
    }
}

func format(_ s: String) -> String {
    return s.uppercased()
}
