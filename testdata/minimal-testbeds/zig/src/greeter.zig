const helpers = @import("helpers.zig");

pub const NameSlice = []const u8;

pub const Tone = enum {
    casual,
    formal,
};

pub const Greeter = struct {
    tone: Tone,

    pub fn greet(self: Greeter, name: []const u8) []const u8 {
        _ = self;
        return format(name);
    }

    pub fn shout(self: Greeter, name: []const u8) []const u8 {
        _ = self;
        return helpers.upper(format(name));
    }

    pub const Stats = struct {
        count: usize,
    };
};

pub fn format(s: []const u8) []const u8 {
    return s;
}
