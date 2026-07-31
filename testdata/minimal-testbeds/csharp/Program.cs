namespace Example.CsharpBed;

public static class Program
{
    public static void Main(string[] args)
    {
        var builder = WebApplication.CreateBuilder(args);
        builder.Services.AddScoped<UserService>();
        builder.Services.AddSingleton<IClock, SystemClock>();
        var app = builder.Build();

        app.MapGet("/health", () => "ok");
        app.MapPost("/echo", (User body, UserService users) => users.Save(body));

        // Keep console Greeter path for legacy locate probes.
        var greeter = new Greeter();
        _ = greeter.Greet("world");

        app.Run();
    }
}
