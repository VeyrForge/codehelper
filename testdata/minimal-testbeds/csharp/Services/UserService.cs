using Microsoft.AspNetCore.Mvc;

namespace Example.CsharpBed;

public class UserService
{
    public string Greet(string name)
    {
        return "Hello, " + name;
    }

    public User Find(int id)
    {
        return new User { Id = id, Name = "user-" + id };
    }

    public User Save(User body)
    {
        return body;
    }
}

public class User
{
    public int Id { get; set; }
    public string Name { get; set; }
}

public interface IClock
{
    long Now();
}

public class SystemClock : IClock
{
    public long Now()
    {
        return 0;
    }
}
